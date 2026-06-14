// Package rlimit_test — black-box tests for the public rlimit facade.
package rlimit_test

import (
	"runtime"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/rlimit"
)

// TestApplyDelegates asserts the facade forwards to the service: an unmapped
// resource yields UnknownResource on Linux and UnsupportedPlatform off it.
func TestApplyDelegates(t *testing.T) {
	t.Parallel()
	err := rlimit.Apply(0, map[rlimit.Resource]rlimit.Limit{
		//: the zero-value resource has no RLIMIT_* mapping.
		coreproc.ResourceUnknown: {Soft: 1, Hard: 1},
	})
	//: select the platform-correct expectation.
	want := coreproc.CodeUnknownResource
	//: off Linux the stub short-circuits before mapping.
	if runtime.GOOS != "linux" {
		//: the non-Linux stub returns UNSUPPORTED_PLATFORM.
		want = coreproc.CodeUnsupportedPlatform
	}
	//: the facade must surface the same typed code the service returns.
	if !errs.HasCode(err, want) {
		//: a mismatch means the facade is not delegating faithfully.
		t.Fatalf("Apply = %v, want code %v", err, want)
	}
}

// wantCore accepts the core LimitValue type; passing a rlimit.Limit to it
// compiles only when the two are the same type (an alias), giving a static proof
// the facade introduces no distinct named type.
func wantCore(v coreproc.LimitValue) coreproc.LimitValue {
	//: return the value unchanged so the caller can assert on the round-trip.
	return v
}

// TestAliasesIdentical asserts the public aliases are the very same types as the
// core proc value types, so values cross the facade boundary without conversion.
func TestAliasesIdentical(t *testing.T) {
	t.Parallel()
	lim := rlimit.Limit{Soft: 1, Hard: 2}
	//: passing the facade alias where the core type is required proves identity.
	core := wantCore(lim)
	//: the round-trip must preserve the value (proves a type alias, not a copy type).
	if core.Soft != 1 || core.Hard != 2 {
		//: a divergence would mean the alias is a distinct named type.
		t.Fatalf("alias round-trip lost data: %+v", core)
	}
	//: LimitInfinity must re-export the core constant unchanged.
	if rlimit.LimitInfinity != coreproc.LimitInfinity {
		//: a divergent constant breaks consumer expectations.
		t.Fatalf("LimitInfinity mismatch")
	}
}
