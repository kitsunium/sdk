package entitlement

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
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
		accounts map[string]CIEntitlementValue
		// wantFetch is whether a network round trip should have happened.
		wantFetch bool
		wantErr   error
		reason    string
	}{
		{
			name:      "outside CI nothing is fetched",
			inCI:      false,
			accounts:  map[string]CIEntitlementValue{"42": {}},
			wantFetch: false,
			wantErr:   ErrCIUnverifiable,
			reason:    "no reason to fetch a key set for a run that cannot prove anything",
		},
		{
			name:      "a roster granting no CI fetches nothing either",
			inCI:      true,
			accounts:  nil,
			wantFetch: false,
			wantErr:   ErrCINotEntitled,
			reason:    "spending round trips to reach a refusal already known is waste",
		},
		{
			name:      "an unreachable key set refuses rather than grants",
			inCI:      true,
			accounts:  map[string]CIEntitlementValue{"42": {}},
			wantFetch: true,
			wantErr:   ErrCIUnverifiable,
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
			}), t.TempDir(), nil, &testProduct)

			grant, err := svc.ciSeat(&RosterValue{CIAccounts: tt.accounts}, now)
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
			}), t.TempDir(), nil, &testProduct)

			keys, err := svc.publishedJWKS()
			if !errors.Is(err, ErrCIUnverifiable) {
				t.Errorf("publishedJWKS() error = %v, want ErrCIUnverifiable (%s)", err, tt.reason)
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
// publishedJWKS reuses s.fetch, which speaks in ErrRosterUnreachable because
// its other caller fetches a roster. Letting that out mislabels the outage —
// GitHub's key set went down, not the licence roster — and it is not merely a
// wording problem: licenseExitCode and licenseAdvice both match
// ErrRosterUnreachable ahead of the CI sentinels, so under a strict roster this
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
				//: ErrRosterUnreachable in the first place.
				return nil, errors.New("dial refused")
			}), t.TempDir(), nil, &testProduct)

			_, err := svc.publishedJWKS()
			//: The CI sentinel must survive: it is what licenseExitCode maps
			//: to LicenseCIRefused under a strict roster.
			if !errors.Is(err, ErrCIUnverifiable) {
				t.Fatalf("publishedJWKS() error = %v, want ErrCIUnverifiable (%s)", err, tt.reason)
			}
			//: ...and the roster sentinel must NOT, or the dispatch reroutes.
			if errors.Is(err, ErrRosterUnreachable) {
				t.Errorf("publishedJWKS() error = %v, want no ErrRosterUnreachable in its chain (%s)", err, tt.reason)
			}
			//: The cause still has to be readable, or the diagnosis is lost.
			if !strings.Contains(err.Error(), "dial refused") {
				t.Errorf("publishedJWKS() = %q, want it to name the transport failure (%s)", err, tt.reason)
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
		roster *RosterValue
		// inCI controls whether the runner variables are present.
		inCI   bool
		want   bool
		reason string
	}{
		{name: "a nil roster cannot have relaxed anything", roster: nil, inCI: true, want: false, reason: "taking the process down over a CI seat is worse than any seat"},
		{name: "an ordinary roster is strict inside CI", roster: &RosterValue{}, inCI: true, want: true, reason: "this is the inversion: a device key must not stand in for a seat"},
		{name: "an ordinary roster leaves a laptop alone", roster: &RosterValue{}, inCI: false, want: false, reason: "a laptop never had a seat to prove"},
		{name: "a relaxed roster falls through inside CI", roster: &RosterValue{CIRelaxed: true}, inCI: true, want: false, reason: "a self-hosted runner carrying a device key is a real deployment, and only the vendor may bless it"},
		{name: "a relaxed roster changes nothing on a laptop", roster: &RosterValue{CIRelaxed: true}, inCI: false, want: false, reason: "relaxing something that was never strict is a no-op"},
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
// reader the roster does, so a JWKS outage carries ErrRosterUnreachable inside
// ErrCIUnverifiable. licenseExitCode matches ErrRosterUnreachable before
// ErrLicenseExpired, so an expired licence on a runner during a GitHub outage
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
			inCI: true, deviceErr: ErrNoLicense, ciErr: ErrCINotEntitled,
			wantText: ErrCINotEntitled.Error(),
			leaked:   ErrCINotEntitled,
			reason:   "the text is the actionable half on a runner; the chain would be a dispatch hazard",
		},
		{
			name: "outside CI nothing is folded in",
			inCI: false, deviceErr: ErrNoLicense, ciErr: ErrCINotEntitled,
			leaked: ErrCINotEntitled,
			reason: "\"not running in GitHub Actions\" is noise on a laptop",
		},
		{
			name: "no seat failure leaves the error untouched",
			inCI: true, deviceErr: ErrNoLicense, ciErr: nil,
			reason: "there is nothing to annotate with",
		},
		{
			//: The hijack. ErrRosterUnreachable is NOT a CI sentinel, it
			//: reaches ciErr through publishedJWKS, and both the exit-code
			//: switch and the advice table match it before ErrLicenseExpired.
			name: "a JWKS outage cannot turn an expired licence into a network failure",
			inCI: true, deviceErr: ErrLicenseExpired,
			ciErr:    fmt.Errorf("%w: %w", ErrCIUnverifiable, ErrRosterUnreachable),
			wantText: ErrRosterUnreachable.Error(),
			leaked:   ErrRosterUnreachable,
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
			//: ...and the text must carry it, or the annotation would be
			//: pointless rather than merely invisible.
			if contains := tt.wantText != "" && strings.Contains(got.Error(), tt.wantText); tt.wantText != "" && !contains {
				t.Errorf("ciContext() = %q, want it to mention %q (%s)", got, tt.wantText, tt.reason)
			}
		})
	}
}
