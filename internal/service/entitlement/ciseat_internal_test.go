package entitlement

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// stubGet adapts a function to the Getter the Service fetches through.
type stubGet func(url string) (*http.Response, error)

// stubGet must satisfy Getter, or the Service could not be pointed at it.
var _ Getter = (*stubGet)(nil)

// Get retrieves a URL.
func (f stubGet) Get(url string) (resp *http.Response, err error) {
	return f(url)
}

// Test_ciSeat pins the refusals that happen BEFORE any network round trip, and
// that each one falls through rather than authorising.
//
// The happy path needs signed fixtures and lives in service_external_test.go;
// what matters here is that the cheap refusals are cheap, and that none of
// them ever returns a usable grant.
// Not parallel: the subtests use t.Setenv, which the testing package refuses
// to combine with parallelism because the environment is process-wide.
func Test_Service_ciSeat(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name string
		// inCI controls whether the runner variables are present.
		inCI bool
		// accounts is the roster's CI block.
		accounts map[string]coreent.CIEntitlementValue
		// wantFetch is whether a network round trip should have happened.
		wantFetch bool
		wantErr   error
		reason    string
	}{
		{
			name:      "outside CI nothing is fetched",
			inCI:      false,
			accounts:  map[string]coreent.CIEntitlementValue{"42": {}},
			wantFetch: false,
			wantErr:   coreent.ErrCIUnverifiable,
			reason:    "no reason to fetch a key set for a run that cannot prove anything",
		},
		{
			name:      "a roster granting no CI fetches nothing either",
			inCI:      true,
			accounts:  nil,
			wantFetch: false,
			wantErr:   coreent.ErrCINotEntitled,
			reason:    "spending round trips to reach a refusal already known is waste",
		},
		{
			name:      "an unreachable key set refuses rather than grants",
			inCI:      true,
			accounts:  map[string]coreent.CIEntitlementValue{"42": {}},
			wantFetch: true,
			wantErr:   coreent.ErrCIUnverifiable,
			reason:    "granting because the check could not run would make blocking one endpoint an entitlement",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, bearer := "", ""
			if tt.inCI {
				url, bearer = "https://x.actions.githubusercontent.com/token", "secret"
			}
			t.Setenv(actionsTokenURLEnv, url)
			t.Setenv(actionsTokenBearerEnv, bearer)

			fetched := false
			svc := NewServiceWithGetter(stubGet(func(string) (*http.Response, error) {
				fetched = true
				//: An endpoint that answers nothing usable is the point: the
				//: key set cannot be obtained, so nothing can be verified.
				return nil, errors.New("unreachable")
			}), stubIdentity{}, nil, &testProduct)

			grant, err := svc.ciSeat(&coreent.RosterValue{CIAccounts: tt.accounts}, now)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ciSeat() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
			}
			//: A refusal must never hand back something a caller could use.
			if grant.Subject != "" || !grant.NotAfter.IsZero() {
				t.Errorf("ciSeat() returned a usable grant on refusal: %+v", grant)
			}
			if fetched != tt.wantFetch {
				t.Errorf("fetched = %v, want %v (%s)", fetched, tt.wantFetch, tt.reason)
			}
		})
	}
}

// Test_publishedJWKS pins that an unusable key set is an error rather than an
// empty map.
//
// Returning no keys would report every token as an unknown kid, which sends
// the caller refreshing a key set that is not the problem.
func Test_Service_publishedJWKS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// body is what the endpoint answers, when it answers at all.
		body string
		// fail makes the fetch itself fail.
		fail   bool
		reason string
	}{
		{
			name:   "an unreachable endpoint",
			fail:   true,
			reason: "unverifiable, so the caller falls back rather than reporting a denial",
		},
		{name: "a key set that is not JSON", body: "<html>portal</html>"},
		{name: "a key set with no usable key", body: `{"keys":[]}`},
		{
			name:   "a key set of the wrong type",
			body:   `{"keys":[{"kty":"EC","kid":"k1"}]}`,
			reason: "nothing here can verify RS256",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := NewServiceWithGetter(stubGet(func(string) (*http.Response, error) {
				if tt.fail {
					return nil, errors.New("unreachable")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(tt.body)),
				}, nil
			}), stubIdentity{}, nil, &testProduct)

			keys, err := svc.publishedJWKS()
			if !errors.Is(err, coreent.ErrCIUnverifiable) {
				t.Errorf("publishedJWKS() error = %v, want coreent.ErrCIUnverifiable (%s)", err, tt.reason)
			}
			//: An empty map would read as "no key matches this kid", which is
			//: a different problem from "there are no keys".
			if keys != nil {
				t.Errorf("publishedJWKS() = %v, want nil on refusal", keys)
			}
		})
	}
}

// Test_Service_publishedJWKS_keepsTheRosterVocabularyIn pins that a JWKS outage
// does not leave here wearing the roster's sentinel.
//
// publishedJWKS reuses s.fetch, which speaks in coreent.ErrRosterUnreachable because
// its other caller fetches a roster. Letting that out mislabels the outage —
// GitHub's key set went down, not the licence roster — and it is not merely a
// wording problem: licenseExitCode and licenseAdvice both match
// coreent.ErrRosterUnreachable ahead of the CI sentinels, so under a strict roster this
// exited "cannot reach the licence roster" and sent its operator to check a
// network path that was working.
func Test_Service_publishedJWKS_keepsTheRosterVocabularyIn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
	}{
		{name: "an unreachable key set is a CI failure and nothing else", reason: "the borrowed reader's vocabulary must not travel out with its error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := NewServiceWithGetter(stubGet(func(string) (*http.Response, error) {
				//: The transport is down, which is how s.fetch produces
				//: coreent.ErrRosterUnreachable in the first place.
				return nil, errors.New("dial refused")
			}), stubIdentity{}, nil, &testProduct)

			_, err := svc.publishedJWKS()
			//: The CI sentinel must survive: it is what licenseExitCode maps
			//: to LicenseCIRefused under a strict roster.
			if !errors.Is(err, coreent.ErrCIUnverifiable) {
				t.Fatalf("publishedJWKS() error = %v, want coreent.ErrCIUnverifiable (%s)", err, tt.reason)
			}
			//: ...and the roster sentinel must NOT, or the dispatch reroutes.
			if errors.Is(err, coreent.ErrRosterUnreachable) {
				t.Errorf("publishedJWKS() error = %v, want no coreent.ErrRosterUnreachable in its chain (%s)", err, tt.reason)
			}
			//: The cause still has to be readable, or the diagnosis is
			//: lost — but it belongs in a FIELD now, not in the sentence.
			//: The sentence is the wire-safe half and names no host.
			if got := probeField(err, "cause"); !strings.Contains(got, "dial refused") {
				t.Errorf("publishedJWKS() cause field = %q, want it to name the transport failure (%s)", got, tt.reason)
			}
			//: Both directions: leaking it and losing it are both defects,
			//: and only one of them is the one everybody remembers.
			if strings.Contains(err.Error(), "dial refused") {
				t.Errorf("publishedJWKS() = %q, want the transport detail OUT of the public sentence (%s)", err, tt.reason)
			}
		})
	}
}

// Test_ciRefusalIsFinal pins the inversion: inside GitHub
// Actions a failed CI seat is a REFUSAL unless the signed roster relaxes it.
//
// Falling through used to be unconditional, which meant one copied device key
// bought unlimited CI with no CI entitlement at all — the hole a scheme built
// to meter CI cannot leave open. The rows below are the whole policy: strict
// where a seat was provable, silent everywhere else, and relaxable only by the
// one document an attacker cannot write.
func Test_ciRefusalIsFinal(t *testing.T) {
	tests := []struct {
		name string
		// roster is what the vendor signed, or nil.
		roster *coreent.RosterValue
		// inCI controls whether the runner variables are present.
		inCI   bool
		want   bool
		reason string
	}{
		{name: "a nil roster cannot have relaxed anything", roster: nil, inCI: true, want: false, reason: "taking the process down over a CI seat is worse than any seat"},
		{name: "an ordinary roster is strict inside CI", roster: &coreent.RosterValue{}, inCI: true, want: true, reason: "this is the inversion: a device key must not stand in for a seat"},
		{name: "an ordinary roster leaves a laptop alone", roster: &coreent.RosterValue{}, inCI: false, want: false, reason: "a laptop never had a seat to prove"},
		{name: "a relaxed roster falls through inside CI", roster: &coreent.RosterValue{CIRelaxed: true}, inCI: true, want: false, reason: "a self-hosted runner carrying a device key is a real deployment, and only the vendor may bless it"},
		{name: "a relaxed roster changes nothing on a laptop", roster: &coreent.RosterValue{CIRelaxed: true}, inCI: false, want: false, reason: "relaxing something that was never strict is a no-op"},
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

			if got := ciRefusalIsFinal(tt.roster); got != tt.want {
				t.Errorf("ciRefusalIsFinal() = %v, want %v (%s)", got, tt.want, tt.reason)
			}
		})
	}
}

// Test_ciContext pins the split that makes this annotation safe: the CI cause
// reaches the MESSAGE and never the error chain.
//
// Wrapping the seat's error would splice its chain into the device refusal, and
// that chain is not CI-only — publishedJWKS fetches through the same bounded
// reader the roster does, so a JWKS outage carries coreent.ErrRosterUnreachable inside
// coreent.ErrCIUnverifiable. licenseExitCode matches coreent.ErrRosterUnreachable before
// coreent.ErrLicenseExpired, so an expired licence on a runner during a GitHub outage
// would have exited with the wrong code and told the operator to check their
// network. The last row is that exact scenario.
func Test_ciContext(t *testing.T) {
	tests := []struct {
		name string
		// inCI controls whether the runner variables are present.
		inCI bool
		// deviceErr is the refusal the device path produced.
		deviceErr error
		// ciErr is the seat failure to fold in, or nil.
		ciErr error
		// wantText is a fragment the message must carry, or "" for none.
		wantText string
		// leaked names a sentinel errors.Is must NOT reach through the
		// result, or nil when there is nothing to check.
		leaked error
		reason string
	}{
		{
			name: "inside CI the seat cause reaches the message",
			inCI: true, deviceErr: coreent.ErrNoLicense, ciErr: coreent.ErrCINotEntitled,
			wantText: coreent.ErrCINotEntitled.Error(),
			leaked:   coreent.ErrCINotEntitled,
			reason:   "the text is the actionable half on a runner; the chain would be a dispatch hazard",
		},
		{
			name: "outside CI nothing is folded in",
			inCI: false, deviceErr: coreent.ErrNoLicense, ciErr: coreent.ErrCINotEntitled,
			leaked: coreent.ErrCINotEntitled,
			reason: "\"not running in GitHub Actions\" is noise on a laptop",
		},
		{
			name: "no seat failure leaves the error untouched",
			inCI: true, deviceErr: coreent.ErrNoLicense, ciErr: nil,
			reason: "there is nothing to annotate with",
		},
		{
			//: The hijack. coreent.ErrRosterUnreachable is NOT a CI sentinel, it
			//: reaches ciErr through publishedJWKS, and both the exit-code
			//: switch and the advice table match it before coreent.ErrLicenseExpired.
			name: "a JWKS outage cannot turn an expired licence into a network failure",
			inCI: true, deviceErr: coreent.ErrLicenseExpired,
			ciErr:    fmt.Errorf("%w: %w", coreent.ErrCIUnverifiable, coreent.ErrRosterUnreachable),
			wantText: coreent.ErrRosterUnreachable.Error(),
			leaked:   coreent.ErrRosterUnreachable,
			reason:   "the dispatch must still resolve to the device refusal, which is the one the operator can act on",
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

			got := ciContext(tt.deviceErr, tt.ciErr)
			//: The device sentinel is what every dispatch downstream matches
			//: on; losing it would reroute the refusal.
			if !errors.Is(got, tt.deviceErr) {
				t.Fatalf("ciContext() = %v, want it to wrap %v (%s)", got, tt.deviceErr, tt.reason)
			}
			//: Nothing from the seat may ever be reachable by errors.Is,
			//: whatever the seat happened to wrap.
			if tt.leaked != nil && errors.Is(got, tt.leaked) {
				t.Errorf("errors.Is(ciContext(), %v) = true, want false — the seat's chain must not be spliced in (%s)", tt.leaked, tt.reason)
			}
			//: ...and the FIELD must carry it, or the annotation would be
			//: pointless rather than merely invisible. It is deliberately
			//: not in the sentence: that half is wire-safe and says only
			//: what the device refusal was.
			if tt.wantText == "" {
				return
			}
			if field := probeField(got, "ci_refusal"); !strings.Contains(field, tt.wantText) {
				t.Errorf("ciContext() ci_refusal field = %q, want it to mention %q (%s)", field, tt.wantText, tt.reason)
			}
			if strings.Contains(got.Error(), tt.wantText) {
				t.Errorf("ciContext() = %q, want the seat's sentence OUT of the device refusal's (%s)", got, tt.reason)
			}
		})
	}
}

// probeField reads one structured field back off an error, or "" when the
// error does not carry it.
//
// Every assertion that used to read a particular out of err.Error() reads it
// here instead: the public sentence is the wire-safe half and deliberately
// names no host, no path and no sentence somebody else wrote.
func probeField(err error, key string) string {
	//: Scan the fields origin-wins concatenated onto this error.
	for _, field := range errs.FieldsOf(err) {
		//: The first match wins; keys are not repeated by any call site here.
		if field.Key() == key {
			return field.StringValue()
		}
	}
	//: Absent, which every caller reports as a failed assertion.
	return ""
}

// Test_ciContext_carriesTheSeatsOwnParticulars pins the half the conversion
// nearly ate.
//
// ciErr.Error() is now the wire-safe sentence, so it names WHICH sentinel the
// seat refused with and not which of the three ways that sentinel was reached
// — the roster carries no CI block, the token was minted for another owner,
// the key set was unreachable. Those live in the seat error's own fields, and
// carrying them is the difference between "the CI seat was refused" and
// something an operator on a runner can act on.
func Test_ciContext_carriesTheSeatsOwnParticulars(t *testing.T) {
	tests := []struct {
		name string
		// seat is the refusal the CI path produced.
		seat error
		// wantDetail is a fragment the ci_detail field must carry.
		wantDetail string
		reason     string
	}{
		{
			name:       "a roster with no CI block at all",
			seat:       refuse(coreent.ErrCINotEntitled, errs.String("stage", "ci_seat"), errs.String("condition", "the roster carries no CI block at all")),
			wantDetail: "the roster carries no CI block at all",
			reason:     "one re-signature undoes it, and nothing else says so",
		},
		{
			name:       "a key set that could not be fetched",
			seat:       refuse(coreent.ErrCIUnverifiable, errs.String("stage", "fetch_jwks"), errs.String("cause", "stage=fetch url=https://x/jwks cause=dial refused")),
			wantDetail: "dial refused",
			reason:     "a GitHub outage and an unentitled account are one sentence apart and two remedies apart",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(actionsTokenURLEnv, "https://x.actions.githubusercontent.com/token")
			t.Setenv(actionsTokenBearerEnv, "runner-secret")

			got := ciContext(coreent.ErrNoLicense, tt.seat)
			//: The device sentinel is what every dispatch downstream matches on.
			if !errors.Is(got, coreent.ErrNoLicense) {
				t.Fatalf("ciContext() = %v, want it to wrap ErrNoLicense (%s)", got, tt.reason)
			}
			if detail := probeField(got, "ci_detail"); !strings.Contains(detail, tt.wantDetail) {
				t.Errorf("ci_detail = %q, want it to carry %q (%s)", detail, tt.wantDetail, tt.reason)
			}
			//: And still not in the sentence, which is the device refusal's.
			if strings.Contains(got.Error(), tt.wantDetail) {
				t.Errorf("ciContext() = %q, want the seat's particulars OUT of the public sentence (%s)", got, tt.reason)
			}
		})
	}
}

// seatWorld is a complete signed world for one CI verification: GitHub's
// published key set and the private half that signs the token it publishes.
//
// Everything is real — a genuine RS256 signature over a genuine compact JWS —
// because the thing under test is a value carried OUT of the verified claims,
// and a stubbed verification would hand it back whatever the stub was told to
// say.
type seatWorld struct {
	// jwks is the key set the JWKS endpoint serves.
	jwks []byte
	// signer is the private half of the one key in that set.
	signer *rsa.PrivateKey
}

// newSeatWorld publishes one RSA key and keeps its private half.
func newSeatWorld(t *testing.T) *seatWorld {
	t.Helper()

	signer, signerErr := rsa.GenerateKey(nil, internalKeyBits)
	//: A failure here is an environment problem, not a test outcome.
	if signerErr != nil {
		t.Fatalf("generating signing key: %v", signerErr)
	}
	jwks, jwksErr := json.Marshal(JWKSValue{Keys: []JWKValue{{
		KeyType:   "RSA",
		KeyID:     "k1",
		Use:       "sig",
		Algorithm: "RS256",
		Modulus:   base64.RawURLEncoding.EncodeToString(signer.N.Bytes()),
		Exponent:  base64.RawURLEncoding.EncodeToString(minimalBigEndian(signer.E)),
	}}})
	//: A failure here is an environment problem, not a test outcome.
	if jwksErr != nil {
		t.Fatalf("marshalling key set: %v", jwksErr)
	}
	//: A key set, and the half that can sign against it.
	return &seatWorld{jwks: jwks, signer: signer}
}

// minimalBigEndian renders an RSA exponent the way a JWK carries one.
func minimalBigEndian(exponent int) []byte {
	var out []byte
	//: Strip leading zero bytes by construction: a JWK's `e` is minimal.
	for exponent > 0 {
		out = append([]byte{byte(exponent & 0xff)}, out...)
		exponent >>= 8
	}
	//: The minimal big-endian encoding.
	return out
}

// mint signs a token the way Actions does, with the window the caller names.
//
// It goes through mintFixtureToken rather than building its own segments, so the
// token this fixture publishes and the one oidc_internal_test.go verifies are the
// same document by construction.
func (w *seatWorld) mint(t *testing.T, issued, expires time.Time, ownerID, audience string) string {
	t.Helper()

	//: A compact JWS indistinguishable from the runner's.
	return mintFixtureToken(t, w.signer, actionsClaimsFixture{
		Issuer:            ActionsIssuer,
		Audience:          audience,
		Subject:           "repo:kodflow/widget:ref:refs/heads/main",
		Repository:        "kodflow/widget",
		RepositoryOwner:   "kodflow",
		RepositoryOwnerID: ownerID,
		IssuedAt:          issued.Unix(),
		ExpiresAt:         expires.Unix(),
	})
}

// Test_Service_ciSeat_boundsTheGrantByItsOwnToken pins that a CI grant dies with
// the proof that established it.
//
// core/grant.go states the invariant in so many words — "a grant may not outlive
// the document that authorised it" — and this was the one call site that broke it.
// GrantDeadline took exactly the two bounds a DEVICE grant rests on, the roster's
// window and the subject's term, and a CI seat rests on a third: the Actions
// token, which GitHub mints for at most maxTokenLifetime. So a seat established by
// a four-minute proof was bounded by the roster instead and lived for hours. An
// exfiltrated token — the exposure oidc.go's package comment accepts and
// states — bought far more than the token's own window.
//
// Both rows are discriminating, and each one catches a different wrong answer.
// The first would read 10 h before the fix, which is the defect. The second is
// what stops the repair from being "always bound by the token": a roster closing
// BEFORE the token still wins, because the tightest bound is the rule and the
// token is a bound rather than the answer.
//
// No clock skew is asserted anywhere, and its absence is the point.
// checkTiming allows clockSkew when ADMITTING a token, which is permissive and
// right; the same allowance on this bound would extend the grant past the proof.
func Test_Service_ciSeat_boundsTheGrantByItsOwnToken(t *testing.T) {
	tests := []struct {
		name string
		// tokenWindow is how far ahead of now the token's own exp sits.
		tokenWindow time.Duration
		// rosterWindow is how far ahead of now the roster's exp sits.
		rosterWindow time.Duration
		// wantBound is which of the two the grant's NotAfter must equal.
		wantBound time.Duration
		reason    string
	}{
		{
			name:         "a token closing before the roster bounds the grant",
			tokenWindow:  4 * time.Minute,
			rosterWindow: 10 * time.Hour,
			wantBound:    4 * time.Minute,
			reason:       "the proof is a document the grant rests on, and a grant may not outlive one",
		},
		{
			name:         "a roster closing before the token still wins",
			tokenWindow:  20 * time.Minute,
			rosterWindow: 5 * time.Minute,
			wantBound:    5 * time.Minute,
			reason:       "the tightest bound is the rule; the token is one of them, never the answer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			//: Both variables, so InCI reports true whatever the ambient
			//: environment says. The URL must satisfy checkTokenURL.
			t.Setenv(actionsTokenURLEnv, "https://pipelines.actions.githubusercontent.com/token")
			t.Setenv(actionsTokenBearerEnv, "runner-secret")

			now := time.Now().Truncate(time.Second)
			world := newSeatWorld(t)
			token := world.mint(t, now.Add(-time.Minute), now.Add(tt.tokenWindow), "42", testProduct.CIAudience)

			//: The roster is handed in as a value: ciSeat takes an
			//: already-authenticated document, so nothing here needs a vendor key.
			roster := &coreent.RosterValue{
				IssuedAt:   now.Add(-time.Minute),
				ExpiresAt:  now.Add(tt.rosterWindow),
				CIAccounts: map[string]coreent.CIEntitlementValue{"42": {}},
			}

			svc := NewServiceWithGetter(stubGet(func(string) (*http.Response, error) {
				//: Only the key set is fetched through the Getter; the token
				//: comes through the bearer transport below.
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(world.jwks)))}, nil
			}), stubIdentity{}, nil, &testProduct).
				WithBearerFetch(func(string, string) (*http.Response, error) {
					//: The mint endpoint's shape, with a real signed token in it.
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"value":"` + token + `"}`))}, nil
				})

			grant, err := svc.ciSeat(roster, now)
			if err != nil {
				t.Fatalf("ciSeat() error = %v, want a grant (%s)", err, tt.reason)
			}
			want := now.Add(tt.wantBound)
			if !grant.NotAfter.Equal(want) {
				t.Errorf("ciSeat() grant.NotAfter = %s, want %s (%s)",
					grant.NotAfter.UTC().Format(time.RFC3339), want.UTC().Format(time.RFC3339), tt.reason)
			}
		})
	}
}

// Test_Service_ciSeat_refusesATokenAlreadyPastItsExpiry closes the gap the
// third bound opened.
//
// checkTiming admits a token until exp + clockSkew, which is permissive and
// correct THERE: a local clock two minutes fast must not reject a token GitHub
// still considers live. But the grant's deadline is bounded by the RAW exp, so
// inside that allowance Verify succeeded and returned a grant whose
// Expired(now) was already true — a successful verification handing back
// authorization every consumer must immediately reject.
//
// Refused rather than extended. Widening the deadline to exp + clockSkew would
// put the grant past the document that authorised it, which is the invariant
// the third bound exists for; and a seat built on a token that has actually
// expired buys a run nothing it could use.
//
// The last row is the control: the skew allowance must keep doing its job for
// a token whose exp is still ahead, which is every ordinary run. Without it
// this test would pass against a change that refused every CI seat.
func Test_Service_ciSeat_refusesATokenAlreadyPastItsExpiry(t *testing.T) {
	tests := []struct {
		name string
		// tokenWindow is how far the token's exp sits from now; negative is
		// past.
		tokenWindow time.Duration
		wantErr     error
		reason      string
	}{
		{
			name:        "expired, but inside the skew allowance",
			tokenWindow: -time.Minute,
			wantErr:     coreent.ErrCIUnverifiable,
			reason:      "checkTiming admits it and the raw exp bounds the grant, so the grant came back already expired",
		},
		{
			name:        "one second past expiry",
			tokenWindow: -time.Second,
			wantErr:     coreent.ErrCIUnverifiable,
			reason:      "inside the allowance and unusable, like the row above",
		},
		{
			name:        "expiring at this very instant",
			tokenWindow: 0,
			wantErr:     coreent.ErrCIUnverifiable,
			reason:      "THE boundary. now == exp gives NotAfter == now, and Expired compares strictly — so the grant reports unexpired while having zero usable lifetime, and every consumer would try to use it",
		},
		{
			name:        "still live",
			tokenWindow: 4 * time.Minute,
			wantErr:     nil,
			reason:      "the control: every ordinary run is this row, and refusing it would be an outage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			//: Both variables, so InCI reports true whatever the ambient
			//: environment says. The URL must satisfy checkTokenURL.
			t.Setenv(actionsTokenURLEnv, "https://pipelines.actions.githubusercontent.com/token")
			t.Setenv(actionsTokenBearerEnv, "runner-secret")

			now := time.Now().Truncate(time.Second)
			world := newSeatWorld(t)
			//: iat well behind exp, so the only thing under test is where exp
			//: sits relative to now.
			token := world.mint(t, now.Add(-10*time.Minute), now.Add(tt.tokenWindow), "42", testProduct.CIAudience)

			roster := &coreent.RosterValue{
				IssuedAt:   now.Add(-time.Minute),
				ExpiresAt:  now.Add(10 * time.Hour),
				CIAccounts: map[string]coreent.CIEntitlementValue{"42": {}},
			}

			svc := NewServiceWithGetter(stubGet(func(string) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(world.jwks)))}, nil
			}), stubIdentity{}, nil, &testProduct).
				WithBearerFetch(func(string, string) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"value":"` + token + `"}`))}, nil
				})

			grant, err := svc.ciSeat(roster, now)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ciSeat() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
				}

				return
			}
			if err != nil {
				t.Fatalf("ciSeat() error = %v, want a grant (%s)", err, tt.reason)
			}
			//: And the grant it does return must be USABLE at now, which is
			//: the property the refused rows above were violating.
			if grant.Expired(now) {
				t.Errorf("ciSeat() returned a grant already expired at %s (NotAfter %s) — a successful verification must not hand back authorization a consumer has to reject (%s)",
					now.UTC().Format(time.RFC3339), grant.NotAfter.UTC().Format(time.RFC3339), tt.reason)
			}
		})
	}
}
