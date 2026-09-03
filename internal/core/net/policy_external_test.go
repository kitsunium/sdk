package net_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestPolicyFuncAdaptsAPlainFunction pins that the func adapter forwards the
// whole request description, including the origin fields a path-only policy
// cannot see.
func TestPolicyFuncAdaptsAPlainFunction(t *testing.T) {
	t.Parallel()
	var seen corenet.RequestValue
	var policy corenet.Policy = corenet.PolicyFunc(func(req corenet.RequestValue) error {
		seen = req
		return nil
	})
	want := corenet.RequestValue{Method: "GET", Scheme: "https", Host: "sdm:8000", EscapedPath: "/v1/subscribers", RawQuery: "purgeFlag=false"}
	if err := policy.Allow(want); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seen != want {
		t.Fatalf("policy saw %+v, want %+v", seen, want)
	}
}

// TestPolicyRefusalPropagates pins that a refusal reaches the caller unchanged,
// so the transport can relabel it as REQUEST_DENIED without losing the reason.
func TestPolicyRefusalPropagates(t *testing.T) {
	t.Parallel()
	var policy corenet.Policy = corenet.PolicyFunc(func(corenet.RequestValue) error {
		return corenet.RequestDenied
	})
	if err := policy.Allow(corenet.RequestValue{Method: "DELETE"}); err == nil {
		t.Fatal("expected the refusal to propagate")
	}
}

// TestAddressStringIsASingleToken pins the rendering used in structured error
// fields, where a multi-token form would need quoting.
func TestAddressStringIsASingleToken(t *testing.T) {
	t.Parallel()
	cases := []struct {
		addr corenet.AddressValue
		want string
	}{
		{corenet.AddressValue{Network: "tcp", Addr: ":8443"}, "tcp://:8443"},
		{corenet.AddressValue{Network: "unix", Addr: "/run/api.sock"}, "unix:///run/api.sock"},
		{corenet.AddressValue{Network: "udp", Addr: "0.0.0.0:53"}, "udp://0.0.0.0:53"},
	}
	for _, tc := range cases {
		if got := tc.addr.String(); got != tc.want {
			t.Fatalf("String() = %q, want %q", got, tc.want)
		}
	}
}
