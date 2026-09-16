// Internal tests: what a GitHub key rotation actually costs a CI seat, and what
// this package does and does not do about an unknown kid.
package entitlement

import (
	"crypto/rsa"
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

// rotatingKeys is one of GitHub's signing keys, published under a kid the caller
// chooses.
//
// seatWorld already does this for a single hard-coded "k1", which is enough for
// every case that is not about rotation and unusable for one that is: a rotation
// is two DIFFERENT kids, and the whole point is which of them the endpoint is
// serving at the moment a token arrives.
type rotatingKeys struct {
	// kid is how a token's header selects this key.
	kid string
	// signer is the private half, so a token can be minted against it.
	signer *rsa.PrivateKey
}

// newRotatingKeys mints one RSA key published under kid.
func newRotatingKeys(t *testing.T, kid string) *rotatingKeys {
	t.Helper()

	signer, signerErr := rsa.GenerateKey(nil, internalKeyBits)
	//: A failure here is an environment problem, not a test outcome.
	if signerErr != nil {
		t.Fatalf("generating signing key %q: %v", kid, signerErr)
	}
	//: A key, and the half that can sign against it.
	return &rotatingKeys{kid: kid, signer: signer}
}

// document renders the JWKS the endpoint serves while this key is published.
func (r *rotatingKeys) document(t *testing.T) string {
	t.Helper()

	raw, marshalErr := json.Marshal(JWKSValue{Keys: []JWKValue{{
		KeyType:   "RSA",
		KeyID:     r.kid,
		Use:       "sig",
		Algorithm: "RS256",
		Modulus:   base64.RawURLEncoding.EncodeToString(r.signer.N.Bytes()),
		Exponent:  base64.RawURLEncoding.EncodeToString(minimalBigEndian(r.signer.E)),
	}}})
	//: A failure here is an environment problem, not a test outcome.
	if marshalErr != nil {
		t.Fatalf("marshalling key set: %v", marshalErr)
	}
	//: The document a publication point serves.
	return string(raw)
}

// mint signs a token naming THIS key's kid in its header.
//
// It cannot go through mintFixtureToken, which hard-codes "k1": the header's kid
// is the input under test here, so it has to be the caller's.
func (r *rotatingKeys) mint(t *testing.T, now time.Time, ownerID string) string {
	t.Helper()

	encode := func(v any) string {
		raw, marshalErr := json.Marshal(v)
		//: A failure here is an environment problem, not a test outcome.
		if marshalErr != nil {
			t.Fatalf("marshalling segment: %v", marshalErr)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	header := encode(jwtHeaderFixture{Algorithm: "RS256", KeyID: r.kid, Type: "JWT"})
	payload := encode(actionsClaimsFixture{
		Issuer:            ActionsIssuer,
		Audience:          testProduct.CIAudience,
		Subject:           "repo:kodflow/widget:ref:refs/heads/main",
		Repository:        "kodflow/widget",
		RepositoryOwner:   "kodflow",
		RepositoryOwnerID: ownerID,
		IssuedAt:          now.Add(-time.Minute).Unix(),
		ExpiresAt:         now.Add(5 * time.Minute).Unix(),
	})
	//: A real RS256 signature over the real signing input, so the verification
	//: under test is the production one.
	signature := signFixture(t, r.signer, header+"."+payload)
	//: The token as a runner would hand it over.
	return header + "." + payload + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// countingKeyEndpoint serves whatever key set is currently published and counts
// every request for it.
//
// The COUNT is the assertion in two of the three cases below, which is why this
// is not a stubGet closure: "how many times was GitHub's host asked" is the
// property, and a closure that merely answered would pass whatever the answer
// cost.
type countingKeyEndpoint struct {
	// served is the document the endpoint currently publishes. Read and
	// written from one goroutine per case, but through atomics anyway: the
	// suite runs under the race detector and a rotation is a write between two
	// reads.
	served atomic.Value
	// requests counts every GET this endpoint answered.
	requests atomic.Int64
}

// Get answers with whatever is currently published, and counts the request.
func (c *countingKeyEndpoint) Get(_ string) (*http.Response, error) {
	c.requests.Add(1)
	document, _ := c.served.Load().(string)
	//: Serve the current key set, as GitHub's host would.
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(document))}, nil
}

// _ asserts the endpoint is usable where a Getter is wanted.
var _ Getter = (*countingKeyEndpoint)(nil)

// seatFixture wires one verification: a roster covering the account, an
// endpoint, and a bearer transport handing over the token.
func seatFixture(t *testing.T, endpoint *countingKeyEndpoint, token string) *Service {
	t.Helper()

	//: The mint endpoint's shape, with the caller's token in it.
	return NewServiceWithGetter(endpoint, stubIdentity{}, nil, &testProduct).
		WithBearerFetch(func(string, string) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"value":"` + token + `"}`))}, nil
		})
}

// seatRoster is a roster covering one CI account, handed in as an already
// authenticated value.
func seatRoster(now time.Time, ownerID string) *coreent.RosterValue {
	//: ciSeat takes an authenticated document, so nothing here needs a vendor
	//: key or a signature.
	return &coreent.RosterValue{
		IssuedAt:   now.Add(-time.Minute),
		ExpiresAt:  now.Add(10 * time.Hour),
		CIAccounts: map[string]coreent.CIEntitlementValue{ownerID: {}},
	}
}

// Test_Service_ciSeat_fetchesTheKeySetOncePerVerification pins the absence the
// comments used to deny.
//
// # What was claimed
//
// selectKey said "The caller refreshes the JWKS once and retries", and
// core/entitlement's ErrCIUnknownKey went further: "refresh the key set once,
// since GitHub rotates keys, then refuse if it still does not appear". No caller
// did either. ciSeat fetches once, calls VerifyCI, and propagates. A comment
// promising a protection that does not exist is the exact class of defect this
// audit is about, so the claim is gone and this test is what keeps its
// replacement answerable to the code.
//
// # Why one fetch is the RIGHT number and not merely the current one
//
// A second GET of the same URL, seconds after the first, from the same process
// and the same network path, returns the same bytes — it cannot make an endpoint
// publish a kid it does not publish. And it would cost something real: the fetch
// currently happens BEFORE any token is examined, so no token can cause a
// request. Keying a refresh on an unknown kid reverses that, which is what the
// next test measures.
func Test_Service_ciSeat_fetchesTheKeySetOncePerVerification(t *testing.T) {
	//: Both variables, so InCI reports true whatever the ambient environment
	//: says. The URL must satisfy checkTokenURL. t.Setenv is why this test is
	//: sequential.
	t.Setenv(actionsTokenURLEnv, "https://pipelines.actions.githubusercontent.com/token")
	t.Setenv(actionsTokenBearerEnv, "runner-secret")

	now := time.Now().Truncate(time.Second)
	published := newRotatingKeys(t, "published")
	rotatedOut := newRotatingKeys(t, "rotated-out")

	endpoint := &countingKeyEndpoint{}
	endpoint.served.Store(published.document(t))

	//: A token naming a kid the served set does not publish — the rotated-out
	//: or forged case, which are indistinguishable from here and must be.
	svc := seatFixture(t, endpoint, rotatedOut.mint(t, now, "42"))

	_, err := svc.ciSeat(seatRoster(now, "42"), now)
	if !errors.Is(err, coreent.ErrCIUnknownKey) {
		t.Fatalf("ciSeat() error = %v, want %v", err, coreent.ErrCIUnknownKey)
	}
	//: ONE request, and the count is the assertion: a refresh-and-retry would
	//: read two, which is what the comments described.
	if got := endpoint.requests.Load(); got != 1 {
		t.Errorf("the key endpoint answered %d requests for one verification, want 1 — "+
			"a second fetch cannot make an endpoint publish a kid it does not publish", got)
	}
}

// Test_Service_ciSeat_doesNotAmplifyARepeatedUnknownKid measures the
// amplification a refresh-on-unknown-kid would buy an attacker.
//
// The fetch happens before any token is examined, so the number of requests this
// package aims at GitHub's host is a function of how many verifications ran and
// of NOTHING the token says. A refresh keyed on the kid changes that: a random
// kid per invocation would buy a request per invocation, at somebody else's host,
// and would then need a rate limit and a timer to be safe again — new state, to
// restore a property the absence of the feature already has.
//
// Three verifications, three requests. The number is linear in verifications and
// independent of the token, which is the property stated as an equality rather
// than as an intention.
func Test_Service_ciSeat_doesNotAmplifyARepeatedUnknownKid(t *testing.T) {
	//: Both variables, so InCI reports true whatever the ambient environment says.
	t.Setenv(actionsTokenURLEnv, "https://pipelines.actions.githubusercontent.com/token")
	t.Setenv(actionsTokenBearerEnv, "runner-secret")

	const rounds int = 3

	now := time.Now().Truncate(time.Second)
	published := newRotatingKeys(t, "published")

	endpoint := &countingKeyEndpoint{}
	endpoint.served.Store(published.document(t))
	roster := seatRoster(now, "42")

	//: A DIFFERENT unknown kid each round, which is the hostile shape: the same
	//: unknown kid could be answered from a negative cache, a fresh one every
	//: time cannot.
	for round := range rounds {
		unknown := newRotatingKeys(t, "unknown-"+string(rune('a'+round)))
		svc := seatFixture(t, endpoint, unknown.mint(t, now, "42"))

		_, err := svc.ciSeat(roster, now)
		//: Every round must refuse, or the round proved nothing about cost.
		if !errors.Is(err, coreent.ErrCIUnknownKey) {
			t.Fatalf("round %d: ciSeat() error = %v, want %v", round, err, coreent.ErrCIUnknownKey)
		}
	}

	//: One request per verification, never two, and never one per kid.
	if got := endpoint.requests.Load(); got != int64(rounds) {
		t.Errorf("the key endpoint answered %d requests for %d verifications, want %d — "+
			"a token must not be able to buy a request", got, rounds, rounds)
	}
}

// Test_Service_ciSeat_picksUpARotationOnTheNextVerification is the remedy, and
// it is the absence of a cache rather than a refresh.
//
// The concern behind the comments was real: a GitHub key rotation must not break
// the CI seat permanently. It does not, and the reason is that this package holds
// NO key-set cache — every verification reads the currently published set. So the
// verification after a rotation sees the new key with no cache to invalidate, no
// timer to wait out and no process to restart.
//
// The two halves are one test on one Service on purpose. The first half
// establishes that the token really was unverifiable against the set published
// then — without it the second half could pass against a world where nothing was
// ever wrong. The second proves the SAME Service recovers, which is exactly what
// a cached set would prevent: this is the test that turns red the day somebody
// adds the cache the old comments implied, and it names the reason in its own
// failure message.
func Test_Service_ciSeat_picksUpARotationOnTheNextVerification(t *testing.T) {
	//: Both variables, so InCI reports true whatever the ambient environment says.
	t.Setenv(actionsTokenURLEnv, "https://pipelines.actions.githubusercontent.com/token")
	t.Setenv(actionsTokenBearerEnv, "runner-secret")

	now := time.Now().Truncate(time.Second)
	outgoing := newRotatingKeys(t, "k-outgoing")
	incoming := newRotatingKeys(t, "k-incoming")

	endpoint := &countingKeyEndpoint{}
	//: Before the rotation reaches the endpoint: the old set is published while
	//: the runner is already being handed tokens signed by the new key.
	endpoint.served.Store(outgoing.document(t))

	token := incoming.mint(t, now, "42")
	svc := seatFixture(t, endpoint, token)
	roster := seatRoster(now, "42")

	_, before := svc.ciSeat(roster, now)
	//: The premise: the token is genuinely unverifiable against what was
	//: published, which is what makes the recovery below meaningful.
	if !errors.Is(before, coreent.ErrCIUnknownKey) {
		t.Fatalf("ciSeat() before the rotation landed = %v, want %v — the premise does not hold",
			before, coreent.ErrCIUnknownKey)
	}

	//: The rotation lands: the endpoint now publishes the new key.
	endpoint.served.Store(incoming.document(t))

	grant, after := svc.ciSeat(roster, now)
	if after != nil {
		t.Fatalf("ciSeat() after the rotation landed = %v, want a grant — the SAME Service must "+
			"pick up a rotation on its next verification, which a cached key set would prevent", after)
	}
	//: A seat, for the run the token names, so the recovery is a real
	//: verification and not merely a nil error.
	if grant.Subject != "ci:kodflow/widget" {
		t.Errorf("ciSeat() grant.Subject = %q, want %q", grant.Subject, "ci:kodflow/widget")
	}
	//: Two verifications, two fetches: the recovery costs exactly the round trip
	//: that makes it possible, and nothing is being cached between them.
	if got := endpoint.requests.Load(); got != 2 {
		t.Errorf("the key endpoint answered %d requests for two verifications, want 2", got)
	}
}
