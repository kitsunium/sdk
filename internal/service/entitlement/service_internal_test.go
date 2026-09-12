package entitlement

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// sampleSubject is a canonical identifier the filesystem guards accept.
const sampleSubject string = "99999999-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

// failingCloser reports an error on Close so the best-effort path is exercised
// rather than assumed.
type failingCloser struct {
	// reader supplies the body content.
	reader io.Reader
}

// Read delegates to the wrapped reader.
func (f failingCloser) Read(p []byte) (int, error) {
	//: Body content is irrelevant here; only Close behaviour is under test.
	return f.reader.Read(p)
}

// Close always fails, modelling a connection torn down mid-flight.
func (f failingCloser) Close() error {
	//: The whole point is that this error must not reach the caller.
	return errors.New("connection reset")
}

// stubRoundTripper implements Getter for the roster fetches under test.
var _ Getter = (*stubRoundTripper)(nil)

// stubRoundTripper serves one canned bundle.
type stubRoundTripper struct {
	// bundle is the signed document served for every request.
	bundle []byte
	// fail makes the request 404, modelling an origin that stopped
	// publishing.
	fail bool
}

// Get answers with the canned bundle.
func (s stubRoundTripper) Get(_ string) (*http.Response, error) {
	//: Model a publication point that no longer serves the bundle.
	if s.fail {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	//: Serve the whole signed document.
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(s.bundle)))}, nil
}

// TestCloseBestEffort pins that a failing Close never becomes the caller's
// error: masking a real verification result with a teardown failure would be
// the worst possible trade.
func TestCloseBestEffort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		closer  io.Closer
		wantErr bool
	}{
		{name: "a clean close is silent", closer: io.NopCloser(strings.NewReader("")), wantErr: false},
		{name: "a failing close is swallowed", closer: failingCloser{reader: strings.NewReader("")}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: Confirm the fixture really does what the case claims, then
			//: assert the helper turns it into nothing observable.
			direct := tt.closer.Close()
			if (direct != nil) != tt.wantErr {
				t.Fatalf("fixture Close() error = %v, want error: %v", direct, tt.wantErr)
			}
			closeBestEffort(tt.closer, "test body")
		})
	}
}

// TestCurrentRoster pins that both artefacts are mandatory: a roster served
// without its signature is unusable, not merely suspicious.
func Test_Service_currentRoster(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating vendor key: %v", err)
	}
	raw, err := json.Marshal(coreent.RosterValue{
		IssuedAt:  now.Add(-time.Hour),
		ExpiresAt: now.Add(coreent.RosterLifetime - time.Hour),
		Subjects:  map[string]coreent.SubjectValue{sampleSubject: {Fingerprint: "SHA256:x"}},
	})
	if err != nil {
		t.Fatalf("marshalling roster: %v", err)
	}

	tests := []struct {
		name    string
		failAll bool
		wantErr error
	}{
		{name: "a signed bundle authenticates"},
		{name: "an origin that stopped publishing yields no decision", failAll: true, wantErr: coreent.ErrRosterUnreachable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bundle, marshalErr := json.Marshal(BundleValue{
				Payload:   base64.StdEncoding.EncodeToString(raw),
				Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw)),
			})
			//: A failure here is an environment problem, not a test outcome.
			if marshalErr != nil {
				t.Fatalf("marshalling bundle: %v", marshalErr)
			}
			svc := NewServiceWithGetter(stubRoundTripper{bundle: bundle, fail: tt.failAll}, stubIdentity{}, pub, &testProduct)
			got, offline, roErr := svc.currentRoster(now)
			//: NewServiceWithGetter leaves the cache disabled, so nothing
			//: here can answer from disk; a true would mean a test
			//: constructor had quietly acquired a real filesystem.
			if offline {
				t.Error("currentRoster() offline = true, want false (the test constructor configures no cache)")
			}
			//: Error cases assert the sentinel, not the wording.
			if tt.wantErr != nil {
				if !errors.Is(roErr, tt.wantErr) {
					t.Errorf("currentRoster() error = %v, want %v", roErr, tt.wantErr)
				}
				return
			}
			if roErr != nil {
				t.Fatalf("currentRoster() error = %v, want nil", roErr)
			}
			if got.Subjects[sampleSubject].Fingerprint != "SHA256:x" {
				t.Errorf("currentRoster() subjects = %v, want the published entry", got.Subjects)
			}
		})
	}
}

// TestMatchSubject pins the order of refusals, so an operator reading an exit
// code learns which of "unknown", "expired", "wrong key" or "no private half"
// happened.
func Test_Service_matchSubject(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		listed  bool
		expiry  time.Time
		wantErr error
	}{
		{name: "an unlisted subject is revoked", listed: false, wantErr: coreent.ErrRevoked},
		{name: "a listed subject with no local key is unlicensed", listed: true, wantErr: coreent.ErrNoLicense},
		{name: "a listed subject past its own term is expired", listed: true, expiry: now.Add(-time.Second), wantErr: coreent.ErrLicenseExpired},
		{name: "a listed subject before its own term is unlicensed, not expired", listed: true, expiry: now.Add(time.Second), wantErr: coreent.ErrNoLicense},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			subjects := map[string]coreent.SubjectValue{}
			//: Listing the subject moves the failure further down the chain.
			if tt.listed {
				subjects[sampleSubject] = coreent.SubjectValue{Fingerprint: "SHA256:whatever", ExpiresAt: tt.expiry}
			}
			//: "no local key" is now something the IDENTITY reports rather
			//: than something a missing file produces — which is the point of
			//: the port: the mechanism no longer has to know what key material
			//: looks like on disk to test what it does when there is none.
			svc := NewServiceWithGetter(stubRoundTripper{},
				stubIdentity{printErr: coreent.ErrNoLicense}, nil, &testProduct)
			_, err := svc.matchSubject(&coreent.RosterValue{Subjects: subjects}, sampleSubject, now)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("matchSubject() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestFetch pins that every transport outcome collapses to "cannot decide"
// rather than leaking as a generic failure, because the caller maps that
// distinction to a dedicated exit code.
func Test_Service_fetch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		wantErr error
	}{
		{name: "a 200 body is returned", status: http.StatusOK},
		{name: "a 404 yields no decision", status: http.StatusNotFound, wantErr: coreent.ErrRosterUnreachable},
		{name: "a 500 yields no decision", status: http.StatusInternalServerError, wantErr: coreent.ErrRosterUnreachable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := NewServiceWithGetter(statusGetter{status: tt.status}, stubIdentity{}, nil, &testProduct)
			body, err := svc.fetch("https://example.invalid/roster.json")
			//: Error cases assert the sentinel, not the wording.
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("fetch() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("fetch() error = %v, want nil", err)
			}
			if string(body) != "payload" {
				t.Errorf("fetch() body = %q, want %q", body, "payload")
			}
		})
	}
}

// statusGetter implements Getter for the fetch status matrix.
var _ Getter = (*statusGetter)(nil)

// statusGetter answers every request with a fixed status and body.
type statusGetter struct {
	// status is the HTTP status served.
	status int
}

// Get returns the canned response.
func (s statusGetter) Get(_ string) (*http.Response, error) {
	//: Body content is fixed so the assertion can be exact.
	return &http.Response{StatusCode: s.status, Body: io.NopCloser(strings.NewReader("payload"))}, nil
}

// bundleGetter serves one canned bundle for every request.
type bundleGetter struct {
	// bundle is the document served.
	bundle []byte
	// calls counts requests, so a test can prove one origin costs ONE GET.
	// It is an atomic because Get must stay a pure lookup as far as the
	// caller is concerned (KTN-FUNC-PUREGET) while still recording that it
	// happened.
	calls atomic.Int64
}

// record notes that a request was made, keeping Get itself free of visible
// side effects.
func (b *bundleGetter) record() {
	b.calls.Add(1)
}

// requests returns how many times the getter was asked.
func (b *bundleGetter) requests() int64 {
	//: Read the tally the caller asserts on.
	return b.calls.Load()
}

// bundleGetter statically satisfies the seam it stands in for, so a
// signature change on Getter breaks here rather than at the call site.
var _ Getter = (*bundleGetter)(nil)

// Get serves the canned bundle.
func (b *bundleGetter) Get(_ string) (*http.Response, error) {
	b.record()
	//: Serve the whole document; there is no second leg to serve.
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(string(b.bundle))),
	}, nil
}

// signBundle wraps a roster and its signature into the one document a
// publication point serves.
func signBundle(t *testing.T, priv ed25519.PrivateKey, roster coreent.RosterValue) []byte {
	t.Helper()

	raw, err := json.Marshal(roster)
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("marshalling roster: %v", err)
	}
	bundle, err := json.Marshal(BundleValue{
		Payload:   base64.StdEncoding.EncodeToString(raw),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw)),
	})
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("marshalling bundle: %v", err)
	}
	//: Return the document a fetcher serves.
	return bundle
}

// Test_Service_rosterFrom pins what ONE origin can and cannot yield: an
// authenticated in-window roster, and nothing else. A forged signature and a
// closed window are both refused HERE rather than by the caller, which is
// what lets currentRoster treat them the same as an unreachable origin and
// simply move on.
//
// It also pins that one origin costs exactly ONE request. That is the whole
// reason the bundle exists: as two objects, roster and signature were cached
// independently for five minutes each, so a new signature could arrive over
// an old roster — a mismatch indistinguishable from forgery, refusing a
// correct roster for roughly 8% of wall-clock time.
func Test_Service_rosterFrom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// forged selects an impostor's signing key.
		forged bool
		// window shifts the roster's validity relative to now.
		window time.Duration
		// wantOK is whether the origin must yield a usable roster.
		wantOK bool
		reason string
	}{
		{name: "a signed in-window bundle authenticates", wantOK: true, reason: "the ordinary path"},
		{name: "a bundle signed by somebody else is refused", forged: true, wantOK: false, reason: "the signature is the only thing that authorises"},
		{name: "a bundle whose window has closed is refused", window: -48 * time.Hour, wantOK: false, reason: "a stalled publisher must not keep authorising"},
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

			signingKey := vendorPriv
			//: A forged case signs with a key the binary does not trust.
			if tt.forged {
				signingKey = attackerPriv
			}

			now := time.Now()
			getter := &bundleGetter{bundle: signBundle(t, signingKey, coreent.RosterValue{
				IssuedAt:  now.Add(-time.Hour + tt.window),
				ExpiresAt: now.Add(time.Hour + tt.window),
				Subjects:  map[string]coreent.SubjectValue{sampleSubject: {Fingerprint: "SHA256:whatever"}},
			})}
			svc := NewServiceWithGetter(getter, stubIdentity{}, vendorPub, &testProduct)

			origin := coreent.OriginValue{Name: "solo", BundleURL: "https://x/roster.signed.json"}
			roster, rosterErr := svc.rosterFrom(origin, now)

			if (rosterErr == nil) != tt.wantOK {
				t.Fatalf("rosterFrom() error = %v, want ok = %v (%s)", rosterErr, tt.wantOK, tt.reason)
			}
			//: Only an authorised roster has subjects worth inspecting.
			if tt.wantOK {
				if _, ok := roster.Subjects[sampleSubject]; !ok {
					t.Errorf("rosterFrom() returned a roster without %q", sampleSubject)
				}
			}
			//: One origin, one request — no second object to fall out of step
			//: with the first.
			if got := getter.requests(); got != 1 {
				t.Errorf("rosterFrom() made %d request(s), want exactly 1 (%s)", got, tt.reason)
			}
		})
	}
}

// Test_Service_rosterFrom_PropagatesFailures pins that an unusable answer
// refuses the whole origin, so the caller moves on to the next one instead
// of authorising on a partial or malformed one.
func Test_Service_rosterFrom_PropagatesFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
		reason string
	}{
		{name: "a 404 from an origin refuses it", status: http.StatusNotFound, reason: "a repository that stopped publishing is unreachable, not empty"},
		{name: "a 500 from an origin refuses it", status: http.StatusInternalServerError, reason: "a broken endpoint decides nothing"},
		{name: "a body that is not a bundle refuses it", status: http.StatusOK, body: "not json at all", reason: "a captive portal answers 200 with HTML"},
		{name: "a bundle with an undecodable payload refuses it", status: http.StatusOK, body: `{"payload":"!!!","sig":"!!!"}`, reason: "a broken publisher is not a forger, but is still unusable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, _, err := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("generating vendor key: %v", err)
			}

			svc := NewServiceWithGetter(&stubStatusGetter{status: tt.status, body: tt.body}, stubIdentity{}, vendorPub, &testProduct)
			origin := coreent.OriginValue{Name: "solo", BundleURL: "https://x/roster.signed.json"}

			_, rosterErr := svc.rosterFrom(origin, time.Now())
			if !errors.Is(rosterErr, coreent.ErrRosterUnreachable) {
				t.Errorf("rosterFrom() error = %v, want coreent.ErrRosterUnreachable (%s)", rosterErr, tt.reason)
			}
		})
	}
}

// stubStatusGetter statically satisfies the seam it stands in for, so a
// signature change on Getter breaks here rather than at the call site.
var _ Getter = (*stubStatusGetter)(nil)

// stubStatusGetter answers every request with one status and one body.
type stubStatusGetter struct {
	// status is returned for every request.
	status int
	// body is returned alongside it.
	body string
}

// Get answers with the configured status and body.
func (s *stubStatusGetter) Get(_ string) (*http.Response, error) {
	//: A non-200 is refused before the body is read; a 200 exercises the
	//: decode path instead.
	return &http.Response{
		StatusCode: s.status,
		Body:       io.NopCloser(strings.NewReader(s.body)),
	}, nil
}

// Test_Service_authorise pins the seat-or-device decision in isolation, which
// is what Verify delegates to so that Verify itself stays the ORDER.
//
// The order in Verify is load-bearing — the update floor runs before the
// subject lookup so an out-of-date binary cannot skip it by going offline —
// and that property is far easier to keep true when the step that does the
// authorising is not interleaved with the steps that sequence it.
func Test_Service_authorise(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name string
		// discoverErr is what DiscoverSubject said, which Verify defers.
		discoverErr error
		// relaxed is what the vendor signed into the roster: it RESTORES the
		// fall-through that strictness is now the default for.
		relaxed bool
		// inCI controls whether the runner variables are present.
		inCI    bool
		wantErr error
		reason  string
	}{
		{
			name:        "a deferred discovery failure is reported once no seat answers",
			discoverErr: coreent.ErrNoLicense,
			wantErr:     coreent.ErrNoLicense,
			reason:      "Verify defers it precisely so the seat can answer first; when it does not, this is the answer",
		},
		{
			name:        "an ordinary roster in CI refuses with the CI cause",
			discoverErr: coreent.ErrNoLicense,
			inCI:        true,
			wantErr:     coreent.ErrCINotEntitled,
			reason:      "strict is the default, so the seat is what was required and the device half is not the answer to report",
		},
		{
			name:        "a relaxed roster in CI reports the device half again",
			discoverErr: coreent.ErrNoLicense,
			relaxed:     true,
			inCI:        true,
			wantErr:     coreent.ErrNoLicense,
			reason:      "only the signed roster can restore the fall-through, and this row is what proves it still can",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, bearer := "", ""
			//: Both variables, or neither: InCI requires the pair.
			if tt.inCI {
				url, bearer = "https://x.actions.githubusercontent.com/token", "runner-secret"
			}
			t.Setenv(actionsTokenURLEnv, url)
			t.Setenv(actionsTokenBearerEnv, bearer)

			//: No CI block, so ciSeat refuses without a round trip and the
			//: decision below is about what happens NEXT rather than about
			//: the seat machinery, which Test_ciSeat already covers.
			roster := &coreent.RosterValue{IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), CIRelaxed: tt.relaxed}

			svc := NewServiceWithGetter(stubRoundTripper{fail: true}, stubIdentity{}, nil, &testProduct)
			_, err := svc.authorise(roster, sampleSubject, tt.discoverErr, now)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("authorise() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
			}
		})
	}
}

// Test_readBounded pins the one cap both untrusted sources share.
//
// It exists as its own test because the cap is the only thing standing between
// this package and a memory-exhaustion lever, and because it now guards TWO
// doors: the roster fetch, where it always lived, and the cache read, where it
// did not — that path used os.ReadFile and loaded a file of any size before
// checking a single byte of it.
func Test_readBounded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// size is how many bytes the source offers.
		size int64
		// wantErr is whether the read must be refused.
		wantErr bool
		reason  string
	}{
		{name: "an ordinary artefact is read whole", size: 1024, reason: "a roster of thousands of subjects fits far inside the cap"},
		{name: "exactly the cap is still an artefact", size: maxArtefactBytes, reason: "the bound is inclusive; refusing here would refuse a legitimate limit case"},
		{name: "one byte past the cap is refused", size: maxArtefactBytes + 1, wantErr: true, reason: "this is the whole point: the source chooses the size, so we choose the ceiling"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: A reader rather than a buffer, so the oversized case does not
			//: allocate the very thing the cap exists to prevent.
			got, err := readBounded(io.LimitReader(zeroReader{}, tt.size), "source")
			if tt.wantErr {
				//: Refused as an unusable artefact, not as a decision.
				if !errors.Is(err, coreent.ErrRosterUnreachable) {
					t.Errorf("readBounded() error = %v, want coreent.ErrRosterUnreachable (%s)", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("readBounded() error = %v, want nil (%s)", err, tt.reason)
			}
			if int64(len(got)) != tt.size {
				t.Errorf("readBounded() read %d bytes, want %d (%s)", len(got), tt.size, tt.reason)
			}
		})
	}
}

// zeroReader yields an endless run of zero bytes.
//
// bytes.Repeat would allocate the oversized case up front, which is exactly the
// allocation the cap under test exists to refuse.
type zeroReader struct{}

// Read fills p with zeros and never ends.
func (zeroReader) Read(p []byte) (n int, err error) {
	//: A full buffer every time; io.LimitReader is what stops it.
	return len(p), nil
}
