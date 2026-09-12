package entitlement_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	entitlement "github.com/kitsunium/sdk/third-party/entitlement"
)

// tokenEndpoint is the shape of URL the Actions runner injects.
const tokenEndpoint string = "https://pipelinesghubeus.actions.githubusercontent.com/abc/token"

// bearerStub answers a mint request with a canned response, and records what
// it was asked for so a test can assert on the audience.
type bearerStub struct {
	// body is the JSON the endpoint returns.
	body string
	// status is the HTTP status it returns.
	status int
	// err makes the request fail at the transport level.
	err error
	// gotURL records the URL the caller requested.
	gotURL string
	// gotBearer records the credential the caller sent.
	gotBearer string
}

// fetch is the BearerFetch this stub provides.
func (b *bearerStub) fetch(url, bearer string) (resp *http.Response, err error) {
	b.gotURL, b.gotBearer = url, bearer
	if b.err != nil {
		return nil, b.err
	}
	status := b.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(b.body)),
	}, nil
}

// TestInCI pins that presence of the runner variables is not itself a claim.
//
// Anyone can set them. What they mean is "a token could be requested here",
// which is exactly why everything that follows rests on a signature instead.
func TestInCI(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		bearer string
		want   bool
	}{
		{name: "both present", url: tokenEndpoint, bearer: "t", want: true},
		{name: "url only", url: tokenEndpoint},
		{name: "bearer only", bearer: "t"},
		{name: "neither"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", tt.url)
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", tt.bearer)

			if got := entitlement.InCI(); got != tt.want {
				t.Errorf("InCI() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRequestActionsToken pins how the token is asked for: where the request
// goes, what it carries, and every answer that is not a token.
func TestRequestActionsToken(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		bearer  string
		stub    bearerStub
		want    string
		wantErr bool
		reason  string
	}{
		{
			name:   "a minted token",
			url:    tokenEndpoint,
			bearer: "runner-secret",
			stub:   bearerStub{body: `{"value":"a.b.c"}`},
			want:   "a.b.c",
		},
		{
			name:    "outside Actions",
			wantErr: true,
			reason:  "unprovable, not refused: the caller falls back to a device",
		},
		{
			name:    "an endpoint that is not GitHub",
			url:     "https://attacker.test/token",
			bearer:  "runner-secret",
			wantErr: true,
			reason:  "the URL is attacker-controlled and the request carries the runner's secret",
		},
		{
			name:    "a workflow without id-token: write",
			url:     tokenEndpoint,
			bearer:  "runner-secret",
			stub:    bearerStub{status: http.StatusForbidden},
			wantErr: true,
			reason:  "403 is what a workflow missing the permission gets",
		},
		{
			name:    "a transport failure",
			url:     tokenEndpoint,
			bearer:  "runner-secret",
			stub:    bearerStub{err: errors.New("boom")},
			wantErr: true,
		},
		{
			name:    "a well-formed response with no token",
			url:     tokenEndpoint,
			bearer:  "runner-secret",
			stub:    bearerStub{body: `{"value":""}`},
			wantErr: true,
			reason:  "verifying an empty string would be worse than refusing",
		},
		{
			name:    "a response that is not JSON",
			url:     tokenEndpoint,
			bearer:  "runner-secret",
			stub:    bearerStub{body: `<html>captive portal</html>`},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", tt.url)
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", tt.bearer)
			stub := tt.stub

			got, err := entitlement.RequestActionsToken(stub.fetch, entitlement.DefaultActionsAudience)
			if tt.wantErr {
				if !errors.Is(err, entitlement.ErrCIUnverifiable) {
					t.Errorf("RequestActionsToken() error = %v, want ErrCIUnverifiable (%s)", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("RequestActionsToken() error = %v, want nil", err)
			}
			if got != tt.want {
				t.Errorf("RequestActionsToken() = %q, want %q", got, tt.want)
			}
			//: The audience is ours and is set here, so a workflow cannot ask
			//: for a token this verifier would then accept for another purpose.
			if !strings.Contains(stub.gotURL, "audience="+entitlement.DefaultActionsAudience) {
				t.Errorf("requested %q, want it to carry our audience", stub.gotURL)
			}
			if stub.gotBearer != tt.bearer {
				t.Errorf("sent bearer %q, want %q", stub.gotBearer, tt.bearer)
			}
		})
	}
}

// TestVerifyCI pins the two-step nature of a free seat: GitHub says the run is
// real, the roster says the account is covered. Neither alone is enough.
func TestVerifyCI(t *testing.T) {
	tests := []struct {
		name    string
		inCI    bool
		roster  *entitlement.RosterValue
		wantErr error
		reason  string
	}{
		{
			name:    "outside Actions",
			inCI:    false,
			roster:  &entitlement.RosterValue{},
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "nothing to prove; the caller falls back to a device",
		},
		{
			name:    "in Actions but the token does not verify",
			inCI:    true,
			roster:  &entitlement.RosterValue{},
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "the stub returns junk, which must not reach the entitlement check",
		},
		{
			//: A roster that lists the account changes nothing while the token
			//: is unverifiable: GitHub's answer and the vendor's are both
			//: required, and this pins that the entitlement cannot stand in
			//: for the signature.
			name: "a listed account does not excuse an unverifiable token",
			inCI: true,
			roster: &entitlement.RosterValue{
				CIAccounts: map[string]entitlement.CIEntitlementValue{"133899878": {}},
			},
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "entitlement is not authentication",
		},
		{
			//: A nil roster would panic on the lookup. Refusing is the only
			//: acceptable answer: this path must never do worse than fall back
			//: to the device check.
			name:    "a nil roster refuses rather than panics",
			inCI:    true,
			roster:  nil,
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "taking the process down over a CI seat is worse than refusing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, bearer := "", ""
			if tt.inCI {
				url, bearer = tokenEndpoint, "runner-secret"
			}
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", url)
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", bearer)
			stub := bearerStub{body: `{"value":"not.a.token"}`}

			_, err := entitlement.VerifyCI(stub.fetch, nil, tt.roster, testProduct.CIAudience, time.Now())
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("VerifyCI() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
			}
		})
	}
}

// TestDefaultBearerFetch pins that the mint request refuses redirects.
//
// checkTokenURL vouches for the URL it is GIVEN; a redirect is a second
// destination nobody checked. Go's default client decides whether to forward
// an Authorization header by comparing HOSTS, not schemes, so a same-host
// https-to-http redirect would put the runner's credential on the wire in
// plaintext.
func TestDefaultBearerFetch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		redirect bool
		wantErr  bool
	}{
		{name: "a direct answer is returned", redirect: false},
		{name: "a redirect is refused", redirect: true, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: A real listener on purpose: DefaultBearerFetch below is
			//: production code that builds its own client, which cannot reach
			//: an in-memory server. The redirect hop is also part of the
			//: contract under test.
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				//: The header must reach the endpoint on the direct path, and
				//: must not survive a hop the caller never vouched for.
				if tt.redirect && r.URL.Path != "/moved" {
					http.Redirect(w, r, server.URL+"/moved", http.StatusFound)
					return
				}
				if got := r.Header.Get("Authorization"); got != "Bearer secret" {
					t.Errorf("Authorization = %q, want the bearer", got)
				}
				fmt.Fprint(w, `{"value":"a.b.c"}`)
			}))
			t.Cleanup(server.Close)

			resp, err := entitlement.DefaultBearerFetch(server.URL+"/token", "secret")
			if resp != nil {
				t.Cleanup(func() {
					if closeErr := resp.Body.Close(); closeErr != nil {
						t.Errorf("closing response: %v", closeErr)
					}
				})
			}
			if tt.wantErr {
				if err == nil {
					t.Error("DefaultBearerFetch() followed a redirect, want a refusal")
				}
				return
			}
			if err != nil {
				t.Fatalf("DefaultBearerFetch() error = %v, want nil", err)
			}
		})
	}
}

// TestRosterValue_CIEntitlementFor pins who the roster grants a free seat to.
//
// The roster is the only document carrying the vendor's signature, so it — not
// GitHub — decides which accounts are covered. GitHub's word is that the run
// is real, not that it is paid for.
func TestRosterValue_CIEntitlementFor(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		accounts  map[string]entitlement.CIEntitlementValue
		accountID string
		wantErr   bool
		reason    string
	}{
		{
			name:      "a listed account inside its term",
			accounts:  map[string]entitlement.CIEntitlementValue{"42": {ExpiresAt: now.Add(24 * time.Hour)}},
			accountID: "42",
		},
		{
			name:      "a listed account with no term recorded",
			accounts:  map[string]entitlement.CIEntitlementValue{"42": {}},
			accountID: "42",
			reason:    "zero means none recorded, never already-expired",
		},
		{
			name:      "an account the roster does not list",
			accounts:  map[string]entitlement.CIEntitlementValue{"42": {}},
			accountID: "99",
			wantErr:   true,
			reason:    "covers a revoked licence, one with no recorded id, and no licence at all",
		},
		{
			name:      "a listed account past its term",
			accounts:  map[string]entitlement.CIEntitlementValue{"42": {ExpiresAt: now.Add(-time.Hour)}},
			accountID: "42",
			wantErr:   true,
			reason:    "a closed term stops CI as surely as it stops a device",
		},
		{
			name:      "an empty account id",
			accounts:  map[string]entitlement.CIEntitlementValue{"": {}},
			accountID: "",
			wantErr:   true,
			reason:    "a token with no owner must not match whatever an empty key holds",
		},
		{
			name:      "no ci block at all",
			accounts:  nil,
			accountID: "42",
			wantErr:   true,
			reason:    "every roster published before the field existed looks like this",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			roster := &entitlement.RosterValue{CIAccounts: tt.accounts}

			_, err := roster.CIEntitlementFor(tt.accountID, now)
			if tt.wantErr {
				if !errors.Is(err, entitlement.ErrCINotEntitled) {
					t.Errorf("CIEntitlementFor() error = %v, want entitlement.ErrCINotEntitled (%s)", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Errorf("CIEntitlementFor() error = %v, want nil (%s)", err, tt.reason)
			}
		})
	}
}
