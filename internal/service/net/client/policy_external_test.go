package client_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/client"
)

// policyCase describes one authorisation scenario.
type policyCase struct {
	name    string
	method  string
	path    string
	wantErr *errs.Error
}

// buildReadOnly assembles the allowlist the SDM integration actually uses.
func buildReadOnly(t *testing.T) corenet.Policy {
	t.Helper()
	allow, err := client.AllowPaths(`/v1/(subscribers|supi/[^/]+|profiles(/[^/]+)?)`)
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	deny, err := client.DenyPaths(`/v1/(authentication|policy|eir|oam).*`)
	if err != nil {
		t.Fatalf("deny: %v", err)
	}
	return client.Policies(client.AllowMethods("GET"), deny, allow)
}

// TestReadOnlyPolicy pins the authorisation surface, including the two traps a
// hand-written allowlist gets wrong: an unanchored pattern and a dot segment.
func TestReadOnlyPolicy(t *testing.T) {
	t.Parallel()
	cases := []policyCase{
		{name: "a listed read is admitted", method: "GET", path: "/v1/subscribers"},
		{name: "a parameterised read is admitted", method: "GET", path: "/v1/supi/208930000100001"},
		{name: "a write verb is refused", method: "DELETE", path: "/v1/subscribers", wantErr: corenet.RequestDenied},
		{name: "an unlisted path is refused", method: "GET", path: "/v1/context-data", wantErr: corenet.RequestDenied},
		{name: "a denied path is refused", method: "GET", path: "/v1/authentication/all", wantErr: corenet.RequestDenied},
		{
			name: "a prefixed path cannot satisfy an anchored pattern",
			//: the constructor anchors, so this must not match /v1/subscribers.
			method: "GET", path: "/evil/v1/subscribers", wantErr: corenet.RequestDenied,
		},
		{
			name: "a literal dot segment is refused before the patterns",
			//: `[^/]+` happily matches "..", so the pattern alone would admit this.
			method: "GET", path: "/v1/supi/..", wantErr: corenet.UnsafePath,
		},
		{
			name: "a percent-encoded dot segment is refused too",
			//: the upstream would decode %2e%2e back into "..".
			method: "GET", path: "/v1/supi/%2e%2e", wantErr: corenet.UnsafePath,
		},
		{name: "a relative path is refused", method: "GET", path: "v1/subscribers", wantErr: corenet.UnsafePath},
		{
			// The wire form is one segment and satisfies `[^/]+`, but an upstream
			// that decodes before routing sees "/v1/supi/a/b" — two segments and
			// a different resource than the policy believed it authorised. The
			// two views disagree, so the only honest answer is to refuse.
			name:   "an encoded separator is refused because the two ends disagree",
			method: "GET", path: "/v1/supi/a%2fb", wantErr: corenet.UnsafePath,
		},
		{
			name:   "an uppercase encoded separator is refused too",
			method: "GET", path: "/v1/supi/a%2Fb", wantErr: corenet.UnsafePath,
		},
		{
			name:   "an encoded backslash is refused as a separator",
			method: "GET", path: "/v1/supi/a%5cb", wantErr: corenet.UnsafePath,
		},
	}
	policy := buildReadOnly(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runPolicyCase(t, policy, tc)
		})
	}
}

// runPolicyCase evaluates one policyCase.
func runPolicyCase(t *testing.T, policy corenet.Policy, tc policyCase) {
	t.Helper()
	err := policy.Allow(corenet.RequestValue{
		Method: tc.method, Scheme: "https", Host: "sdm:8000", EscapedPath: tc.path,
	})
	//: an admitted request must produce no error at all.
	if tc.wantErr == nil {
		if err != nil {
			t.Fatalf("expected admission, got %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("expected a refusal, the request was admitted")
	}
	if !errs.HasCode(err, codeOf(t, tc.wantErr)) {
		t.Fatalf("expected %v, got %v", tc.wantErr, err)
	}
}

// codeOf extracts the dotted-quad code of a sentinel.
func codeOf(t *testing.T, sentinel error) errs.Code {
	t.Helper()
	code, ok := errs.CodeOf(sentinel)
	if !ok {
		t.Fatalf("sentinel %v carries no code", sentinel)
	}
	return code
}

// TestEmptyAllowlistRefusesEverything pins the classic failure mode: an
// allowlist whose contents went missing — a renamed config key, a slice never
// populated — must close, never turn into a passthrough.
func TestEmptyAllowlistRefusesEverything(t *testing.T) {
	t.Parallel()
	req := corenet.RequestValue{Method: "GET", EscapedPath: "/v1/subscribers"}

	empty, err := client.AllowPaths()
	if err != nil {
		t.Fatalf("AllowPaths(): %v", err)
	}
	if empty.Allow(req) == nil {
		t.Fatal("an empty path allowlist admitted a request")
	}
	if client.AllowMethods().Allow(req) == nil {
		t.Fatal("an empty method allowlist admitted a request")
	}
	if client.Policies().Allow(req) == nil {
		t.Fatal("an empty conjunction admitted a request")
	}
}

// TestMalformedPatternFailsAtConstruction pins that a bad pattern is caught when
// the policy is built, not on the first request that happens to exercise it.
func TestMalformedPatternFailsAtConstruction(t *testing.T) {
	t.Parallel()
	if _, err := client.AllowPaths(`/v1/[unterminated`); err == nil {
		t.Fatal("expected an uncompilable pattern to be refused")
	}
}
