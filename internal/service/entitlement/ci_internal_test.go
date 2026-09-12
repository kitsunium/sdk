package entitlement

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// Test_checkTokenURL pins where the runner's bearer credential may be sent.
//
// The mint URL arrives as an environment variable and the request carries the
// one secret the runner holds, so an unchecked URL is a way to exfiltrate that
// secret to any host by setting a variable.
func Test_checkTokenURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantErr bool
		reason  string
	}{
		{name: "the real endpoint", raw: "https://pipelinesghubeus.actions.githubusercontent.com/abc/token"},
		{name: "the bare host", raw: "https://actions.githubusercontent.com/token"},
		{
			name:    "plain http",
			raw:     "http://actions.githubusercontent.com/token",
			wantErr: true,
			reason:  "would put the bearer credential on the wire",
		},
		{
			name:    "another host entirely",
			raw:     "https://attacker.test/token",
			wantErr: true,
			reason:  "the variable is attacker-controlled; the destination must not be",
		},
		{
			//: The classic suffix-check bypass: the legitimate host appears in
			//: the string, but the registrable domain is the attacker's.
			name:    "the host as a prefix of an attacker domain",
			raw:     "https://actions.githubusercontent.com.attacker.test/token",
			wantErr: true,
			reason:  "Contains would accept this; a suffix match on a dotted prefix does not",
		},
		{
			name:    "a lookalike subdomain",
			raw:     "https://evil-actions.githubusercontent.com.attacker.test/token",
			wantErr: true,
		},
		{
			//: Userinfo does not change Hostname(), so "allowed@evil" targets
			//: evil and must be refused; the reverse still targets the
			//: allowed host and is fine.
			name:    "an allowed host as userinfo on an attacker host",
			raw:     "https://actions.githubusercontent.com@attacker.test/token",
			wantErr: true,
			reason:  "the host is attacker.test whatever precedes the @",
		},
		{
			name: "an attacker host as userinfo on the allowed host",
			raw:  "https://attacker.test@actions.githubusercontent.com/token",
		},
		{
			//: A trailing dot is a distinct hostname to the resolver and does
			//: not match the suffix, so it fails closed.
			name:    "a trailing dot on the host",
			raw:     "https://actions.githubusercontent.com./token",
			wantErr: true,
		},
		{
			//: Unicode lookalikes cannot produce the required ASCII suffix.
			name:    "an IDN lookalike",
			raw:     "https://actions.githubusercontent.cοm/token",
			wantErr: true,
		},
		{
			name: "an explicit port on the allowed host",
			raw:  "https://x.actions.githubusercontent.com:443/token",
		},
		{name: "not a url at all", raw: "://", wantErr: true},
		{name: "empty", raw: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := checkTokenURL(tt.raw)
			if tt.wantErr {
				if !errors.Is(err, coreent.ErrCIUnverifiable) {
					t.Errorf("checkTokenURL(%q) error = %v, want coreent.ErrCIUnverifiable (%s)", tt.raw, err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Errorf("checkTokenURL(%q) error = %v, want nil", tt.raw, err)
			}
		})
	}
}

// Test_tokenResponseBody pins every answer a token cannot be read from,
// including the case a caller cannot reach through RequestActionsToken: no way
// to send the credential at all.
func Test_tokenResponseBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		get     BearerFetch
		want    string
		wantErr bool
		reason  string
	}{
		{
			name: "a minted token",
			get: func(string, string) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"value":"a.b.c"}`)),
				}, nil
			},
			want: "a.b.c",
		},
		{
			name:    "no way to send the credential",
			get:     nil,
			wantErr: true,
			reason:  "an unauthenticated request would only ever return 401",
		},
		{
			name: "a response larger than any token",
			get: func(string, string) (*http.Response, error) {
				//: One byte past the cap, so the check itself is exercised
				//: rather than the JSON decoder choking first.
				body := `{"value":"` + strings.Repeat("A", int(maxTokenResponseBytes)) + `"}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			},
			wantErr: true,
			reason:  "an untrusted endpoint must not choose how much memory we spend",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body, err := tokenResponseBody(tt.get, "https://x.actions.githubusercontent.com/token", "secret")
			if tt.wantErr {
				if !errors.Is(err, coreent.ErrCIUnverifiable) {
					t.Errorf("tokenResponseBody() error = %v, want coreent.ErrCIUnverifiable (%s)", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("tokenResponseBody() error = %v, want nil", err)
			}
			if !strings.Contains(string(body), tt.want) {
				t.Errorf("tokenResponseBody() = %q, want it to carry %q", body, tt.want)
			}
		})
	}
}

// Test_fetchToken pins what is read OUT of a body the transport already
// accepted: a token, or a refusal that never returns an empty string as one.
func Test_fetchToken(t *testing.T) {
	t.Parallel()

	body := func(payload string) BearerFetch {
		return func(string, string) (resp *http.Response, err error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(payload)),
			}, nil
		}
	}

	tests := []struct {
		name    string
		payload string
		want    string
		wantErr bool
		reason  string
	}{
		{name: "a minted token", payload: `{"value":"a.b.c"}`, want: "a.b.c"},
		{
			name:    "a well-formed response with no token",
			payload: `{"value":""}`,
			wantErr: true,
			reason:  "verifying an empty string would be worse than refusing",
		},
		{
			name:    "a response with no value field at all",
			payload: `{}`,
			wantErr: true,
		},
		{
			name:    "a captive portal page",
			payload: `<html>sign in</html>`,
			wantErr: true,
			reason:  "not JSON, and must not reach the verifier",
		},
		{
			name:    "two documents",
			payload: `{"value":"a.b.c"}{"value":"d.e.f"}`,
			wantErr: true,
			reason:  "showing a reader one token and a verifier another",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := fetchToken(body(tt.payload), "https://x.actions.githubusercontent.com/token", "secret")
			if tt.wantErr {
				if !errors.Is(err, coreent.ErrCIUnverifiable) {
					t.Errorf("fetchToken() error = %v, want coreent.ErrCIUnverifiable (%s)", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("fetchToken() error = %v, want nil", err)
			}
			if got != tt.want {
				t.Errorf("fetchToken() = %q, want %q", got, tt.want)
			}
		})
	}
}
