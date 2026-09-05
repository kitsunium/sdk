// Package net_test — the outbound authorisation port adapter.
package net_test

import (
	"errors"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Test_PolicyFunc_Allow pins that the adapter forwards the whole request
// description — including the origin fields a path-only policy cannot see — and
// returns the verdict unchanged.
//
// Both halves matter. A policy that only ever saw the path could not tell an
// allowed path on the configured host from the same path on an attacker's, and
// a refusal that did not propagate verbatim would leave the transport unable to
// relabel it as REQUEST_DENIED without losing the reason.
func Test_PolicyFunc_Allow(t *testing.T) {
	t.Parallel()
	custom := errors.New("refused by a custom rule")
	type tc struct {
		name string
		req  corenet.RequestValue
		ret  error
	}
	tests := []tc{
		{
			name: "an allowed request carrying every field",
			req: corenet.RequestValue{
				Method: "GET", Scheme: "https", Host: "sdm:8000",
				EscapedPath: "/v1/subscribers", RawQuery: "purgeFlag=false",
			},
		},
		{
			name: "a refusal with the domain sentinel",
			req:  corenet.RequestValue{Method: "DELETE", EscapedPath: "/v1/subscribers/1"},
			ret:  corenet.RequestDenied,
		},
		{
			//: a policy may refuse for its own reasons; the adapter must not
			//: normalise that into the domain sentinel.
			name: "a refusal with a caller's own error",
			req:  corenet.RequestValue{Method: "POST"},
			ret:  custom,
		},
		{
			name: "the zero request",
			req:  corenet.RequestValue{},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		calls := 0
		var seen corenet.RequestValue
		policy := corenet.Policy(corenet.PolicyFunc(func(req corenet.RequestValue) error {
			calls++
			seen = req
			return c.ret
		}))

		err := policy.Allow(c.req)

		if !errors.Is(err, c.ret) {
			t.Fatalf("Allow = %v, want %v", err, c.ret)
		}
		if calls != 1 {
			t.Errorf("the wrapped function ran %d times, want 1", calls)
		}
		//: RequestValue is comparable, so a whole-value check catches a field
		//: the adapter dropped without enumerating them.
		if seen != c.req {
			t.Errorf("the policy saw %+v, want %+v", seen, c.req)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
