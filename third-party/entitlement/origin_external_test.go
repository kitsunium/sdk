package entitlement_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	entitlement "github.com/kitsunium/sdk/third-party/entitlement"
)

// originState is what one origin serves in a fallback test.
type originState int

const (
	// originDown answers nothing: a transport failure.
	originDown originState = iota
	// originStale serves a correctly signed roster whose window has closed.
	// This is the case a naive "first origin that answers" would get wrong:
	// the bytes verify, so nothing about the response looks broken, yet
	// accepting it refuses a licence the next origin would have approved.
	originStale
	// originForged serves a roster signed by somebody else's key.
	originForged
	// originHealthy serves a current, correctly signed roster.
	originHealthy
)

// multiOriginGetter answers per-origin, keyed by a marker in the URL, so a
// test can put a different failure at each publication point.
type multiOriginGetter struct {
	// states maps an origin name to what that origin serves.
	states map[string]originState
	// current is the signed bundle a healthy origin returns.
	current []byte
	// stale is the signed bundle a stalled origin returns.
	stale []byte
	// forged is the bundle signed with the wrong key.
	forged []byte
	// mu guards tried: the roster and signature legs are fetched
	// concurrently, so Get runs on two goroutines at once.
	mu sync.Mutex
	// tried records every origin the service actually reached, in order,
	// so a test can assert it stopped at the first usable one.
	tried []string
}

// record notes that an origin was reached. It is separate from Get so the
// getter itself stays a pure lookup (KTN-FUNC-PUREGET) and so the locking
// lives in one place.
func (m *multiOriginGetter) record(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tried = append(m.tried, name)
}

// reached returns the origins the service actually asked, in order.
func (m *multiOriginGetter) reached() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	//: Copy under the lock so the caller cannot race a late fetch.
	return slices.Clone(m.tried)
}

// Get serves whatever the origin named in the URL is configured to serve.
func (m *multiOriginGetter) Get(url string) (*http.Response, error) {
	name := originNameFromURL(url)
	//: One request per origin now: the bundle carries both halves, so there
	//: is no second leg to discount.
	m.record(name)

	//: Dispatch based on the variant to apply the correct logic.
	switch m.states[name] {
	//: A dead endpoint fails at the transport layer.
	case originDown:
		//: No response at all.
		return nil, errors.New("dial refused")
	//: A stalled publisher still serves a valid signature.
	case originStale:
		//: Serve the expired bundle.
		return okResponse(m.stale), nil
	//: A hostile endpoint serves its own signature.
	case originForged:
		//: Serve the bundle signed with the wrong key.
		return okResponse(m.forged), nil
	//: A healthy origin serves the current bundle.
	default:
		//: Serve the in-window bundle.
		return okResponse(m.current), nil
	}
}

// okResponse wraps a body in a 200.
func okResponse(body []byte) *http.Response {
	//: Callers only ever need the status and the body.
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
}

// originNameFromURL recovers the origin name a test URL was built with.
func originNameFromURL(url string) string {
	trimmed := strings.TrimPrefix(url, "https://")
	name, _, _ := strings.Cut(trimmed, "/")
	//: The host IS the origin name in these fixtures.
	return name
}

// testOrigins builds an origin list whose URLs encode the given names.
func testOrigins(names ...string) []entitlement.OriginValue {
	origins := make([]entitlement.OriginValue, 0, len(names))
	//: One origin per name, in the order the service must try them.
	for _, name := range names {
		origins = append(origins, entitlement.OriginValue{
			Name:      name,
			BundleURL: "https://" + name + "/roster.signed.json",
		})
	}
	//: Return the ordered publication points.
	return origins
}

// signPair marshals a roster and wraps it with its signature in the single
// bundle a publication point serves.
func signPair(t *testing.T, priv ed25519.PrivateKey, roster entitlement.RosterValue) []byte {
	t.Helper()

	raw, err := json.Marshal(roster)
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("marshalling roster: %v", err)
	}
	bundle, err := json.Marshal(entitlement.BundleValue{
		Payload:   base64.StdEncoding.EncodeToString(raw),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw)),
	})
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("marshalling bundle: %v", err)
	}
	//: Return the one document a fetcher serves.
	return bundle
}

// TestVerifyFallsBackAcrossOrigins pins the availability property the origin
// list exists for, and the rule that makes it correct.
//
// The rule is that an origin must be SKIPPED for any reason it cannot
// authorise — unreachable, stale, or forged alike — not just for failing to
// answer. A stalled publisher is the dangerous case: its roster is perfectly
// well-signed, so a "first origin that answers" strategy would take it,
// notice the closed window, and refuse a licence the very next origin would
// have approved. That is exactly how a publication outage on one branch
// would take down every client, which is what the second origin exists to
// prevent.
func TestVerifyFallsBackAcrossOrigins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		states    map[string]originState
		order     []string
		wantOK    bool
		wantTried []string
		reason    string
	}{
		{
			name:      "the first healthy origin wins and the rest are never asked",
			states:    map[string]originState{"a": originHealthy, "b": originHealthy},
			order:     []string{"a", "b"},
			wantOK:    true,
			wantTried: []string{"a"},
			reason:    "an origin that authorises ends the search",
		},
		{
			name:      "an unreachable origin falls through to the next",
			states:    map[string]originState{"a": originDown, "b": originHealthy},
			order:     []string{"a", "b"},
			wantOK:    true,
			wantTried: []string{"a", "b"},
			reason:    "a dead endpoint must not be fatal when another is live",
		},
		{
			name:      "a stale origin falls through instead of refusing",
			states:    map[string]originState{"a": originStale, "b": originHealthy},
			order:     []string{"a", "b"},
			wantOK:    true,
			wantTried: []string{"a", "b"},
			reason:    "a stalled publisher serves a valid signature over a closed window",
		},
		{
			name:      "a forged origin falls through instead of refusing",
			states:    map[string]originState{"a": originForged, "b": originHealthy},
			order:     []string{"a", "b"},
			wantOK:    true,
			wantTried: []string{"a", "b"},
			reason:    "a hostile endpoint must not be able to deny service either",
		},
		{
			name:      "every origin unusable refuses",
			states:    map[string]originState{"a": originStale, "b": originDown, "c": originForged},
			order:     []string{"a", "b", "c"},
			wantOK:    false,
			wantTried: []string{"a", "b", "c"},
			reason:    "with nothing to authenticate against, refusing is the only safe answer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, vendorPriv, err := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("generating vendor key: %v", err)
			}
			_, attackerPriv, err := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("generating attacker key: %v", err)
			}

			dir := t.TempDir()
			fingerprint := enrol(t, dir, sampleUUID)
			now := time.Now()
			subjects := map[string]entitlement.SubjectValue{sampleUUID: {Fingerprint: fingerprint}}

			current := entitlement.RosterValue{IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), Subjects: subjects}
			//: A window that closed before `now`: signed correctly, but past.
			expired := entitlement.RosterValue{IssuedAt: now.Add(-48 * time.Hour), ExpiresAt: now.Add(-24 * time.Hour), Subjects: subjects}

			getter := &multiOriginGetter{
				states:  tt.states,
				current: signPair(t, vendorPriv, current),
				stale:   signPair(t, vendorPriv, expired),
				forged:  signPair(t, attackerPriv, current),
			}

			svc := entitlement.NewServiceWithOrigins(getter, dir, vendorPub, testOrigins(tt.order...))
			_, verifyErr := svc.Verify(now)

			if (verifyErr == nil) != tt.wantOK {
				t.Errorf("Verify() error = %v, want ok = %v (%s)", verifyErr, tt.wantOK, tt.reason)
			}
			if tried := getter.reached(); strings.Join(tried, ",") != strings.Join(tt.wantTried, ",") {
				t.Errorf("origins tried = %v, want %v (%s)", tried, tt.wantTried, tt.reason)
			}
		})
	}
}

// TestVerifyWithNoOriginsRefuses pins that an empty list refuses rather than
// silently authorising. A build that lost its origins has nothing to
// authenticate against, which must fail closed like every other unknown.
func TestVerifyWithNoOriginsRefuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
	}{
		{name: "no publication point configured", reason: "nothing to authenticate against must never mean authorised"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, _, err := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("generating vendor key: %v", err)
			}
			dir := t.TempDir()
			enrol(t, dir, sampleUUID)

			svc := entitlement.NewServiceWithOrigins(&multiOriginGetter{}, dir, vendorPub, nil)
			_, verifyErr := svc.Verify(time.Now())

			if !errors.Is(verifyErr, entitlement.ErrRosterUnreachable) {
				t.Errorf("Verify() error = %v, want ErrRosterUnreachable (%s)", verifyErr, tt.reason)
			}
		})
	}
}

// TestOriginsAreDistinct pins the two properties the shipped list must have,
// which are NOT the same property:
//
//   - Publication redundancy: no two origins are the same URL. Two branches
//     of one repository count here — that is what survives a ruleset which
//     refuses the signing bot's push to the default branch, which is the
//     failure that actually happened.
//   - Infrastructure redundancy: at least two DIFFERENT hosts. Branches on
//     one host share an outage of that host, so a list where every entry
//     resolved to raw.githubusercontent.com would look redundant while being
//     a single point of failure.
//
// A list satisfying only the first is a real improvement and still not
// enough, which is why both are asserted separately.
func TestOriginsAreDistinct(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		origins  []entitlement.OriginValue
		wantFail bool
		reason   string
	}{
		{
			name: "two distinct hosts pass",
			origins: []entitlement.OriginValue{
				{Name: "branch", BundleURL: "https://raw.example.invalid/p/roster.signed.json"},
				{Name: "pages", BundleURL: "https://pages.example.invalid/p/roster.signed.json"},
			},
			reason: "independent publication paths on independent hosts",
		},
		{
			name: "one host is refused however many entries",
			origins: []entitlement.OriginValue{
				{Name: "branch", BundleURL: "https://one.example.invalid/a/roster.signed.json"},
				{Name: "other", BundleURL: "https://one.example.invalid/b/roster.signed.json"},
			},
			wantFail: true,
			reason:   "same host means one outage takes them all down",
		},
		{
			name: "the same URL twice is one origin listed twice",
			origins: []entitlement.OriginValue{
				{Name: "a", BundleURL: "https://one.example.invalid/roster.signed.json"},
				{Name: "b", BundleURL: "https://one.example.invalid/roster.signed.json"},
			},
			wantFail: true,
			reason:   "a duplicate URL is not a fallback",
		},
		{
			name: "a duplicate name is refused",
			origins: []entitlement.OriginValue{
				{Name: "dup", BundleURL: "https://one.example.invalid/roster.signed.json"},
				{Name: "dup", BundleURL: "https://two.example.invalid/roster.signed.json"},
			},
			wantFail: true,
			reason:   "two failures must not be indistinguishable in a log",
		},
		{
			name:     "an empty field is refused",
			origins:  []entitlement.OriginValue{{Name: "", BundleURL: "https://one.example.invalid/r.json"}},
			wantFail: true,
			reason:   "an origin that cannot be named cannot be reported when it fails",
		},
		{
			name:     "no origins at all is refused",
			origins:  nil,
			wantFail: true,
			reason:   "zero hosts is fewer than two",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: The source implementation asserted these over its own shipped
			//: constants. Moving the origins to the caller would have moved
			//: the property out of reach with them, so it became a method any
			//: consumer can call — and this is its suite.
			product := entitlement.ProductValue{Name: "p", Origins: tt.origins}
			err := product.Validate()
			if (err != nil) != tt.wantFail {
				t.Errorf("Validate() error = %v, wantFail %t (%s)", err, tt.wantFail, tt.reason)
			}
		})
	}
}

// TestNewServiceWithOrigins pins the constructor the fallback tests depend
// on: it must honour the origin list it is handed rather than the shipped
// one, otherwise every test above would silently be exercising production
// endpoints.
func TestNewServiceWithOrigins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		origins   []string
		wantTried []string
		reason    string
	}{
		{name: "the injected list is what gets asked", origins: []string{"only-this-one"}, wantTried: []string{"only-this-one"}, reason: "a constructor that fell back to Origins would reach the network"},
		{name: "order is preserved", origins: []string{"first", "second"}, wantTried: []string{"first", "second"}, reason: "the list is a priority order, not a set"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, _, err := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("generating vendor key: %v", err)
			}
			dir := t.TempDir()
			enrol(t, dir, sampleUUID)

			//: Every origin down, so all of them are tried and the list is
			//: fully observable.
			states := map[string]originState{}
			for _, name := range tt.origins {
				states[name] = originDown
			}
			getter := &multiOriginGetter{states: states}

			svc := entitlement.NewServiceWithOrigins(getter, dir, vendorPub, testOrigins(tt.origins...))
			_, verifyErr := svc.Verify(time.Now())

			//: Every origin was down, so this must refuse.
			if verifyErr == nil {
				t.Errorf("Verify() = nil error, want a refusal (%s)", tt.reason)
			}
			if tried := getter.reached(); strings.Join(tried, ",") != strings.Join(tt.wantTried, ",") {
				t.Errorf("origins tried = %v, want %v (%s)", tried, tt.wantTried, tt.reason)
			}
		})
	}
}

// TestBundleRequiresBothHalves pins that a bundle missing either half is
// refused. Both are mandatory: bytes without a signature authorise nothing,
// and a signature over nothing authorises nothing either.
//
// The halves now travel together precisely so they cannot go missing
// independently in transit — this test covers a broken publisher, which is
// the only way it can still happen.
func TestBundleRequiresBothHalves(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		dropSig bool
		dropRos bool
		reason  string
	}{
		{name: "a bundle with no signature refuses", dropSig: true, reason: "unsigned bytes authorise nothing"},
		{name: "a bundle with no payload refuses", dropRos: true, reason: "a signature over nothing authorises nothing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, vendorPriv, err := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("generating vendor key: %v", err)
			}
			dir := t.TempDir()
			fingerprint := enrol(t, dir, sampleUUID)
			now := time.Now()

			raw, err := json.Marshal(entitlement.RosterValue{
				IssuedAt:  now.Add(-time.Hour),
				ExpiresAt: now.Add(time.Hour),
				Subjects:  map[string]entitlement.SubjectValue{sampleUUID: {Fingerprint: fingerprint}},
			})
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("marshalling roster: %v", err)
			}

			value := entitlement.BundleValue{
				Payload:   base64.StdEncoding.EncodeToString(raw),
				Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(vendorPriv, raw)),
			}
			//: Drop whichever half this case is about, leaving the other
			//: perfectly valid so only the absence is under test.
			if tt.dropSig {
				value.Signature = ""
			}
			if tt.dropRos {
				value.Payload = ""
			}
			bundle, err := json.Marshal(value)
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("marshalling bundle: %v", err)
			}

			getter := &multiOriginGetter{
				states:  map[string]originState{"solo": originHealthy},
				current: bundle,
			}
			svc := entitlement.NewServiceWithOrigins(getter, dir, vendorPub, testOrigins("solo"))

			if _, verifyErr := svc.Verify(now); verifyErr == nil {
				t.Errorf("Verify() = nil error, want a refusal (%s)", tt.reason)
			}
		})
	}
}
