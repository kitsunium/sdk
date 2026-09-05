// Package rlimit_test — black-box tests for the public rlimit facade.
package rlimit_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/rlimit"
)

// TestApplyDelegates asserts the facade forwards to the service rather than
// re-implementing any part of it: the typed code that comes back is the
// service's own. A facade that swallowed or re-wrapped it would leave callers
// unable to tell a bad resource from an unsupported platform.
func TestApplyDelegates(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		pid     int
		limits  map[rlimit.Resource]rlimit.Limit
		native  errs.Code
		foreign errs.Code
	}
	tests := []tc{
		{
			//: the zero-value resource has no RLIMIT_* mapping, so a native
			//: platform gets as far as the mapping table and refuses there.
			name:    "an unmapped resource",
			limits:  map[rlimit.Resource]rlimit.Limit{coreproc.ResourceUnknown: {Soft: 1, Hard: 1}},
			native:  coreproc.CodeUnknownResource,
			foreign: coreproc.CodeUnsupportedPlatform,
		},
		{
			name:    "an out-of-range resource",
			limits:  map[rlimit.Resource]rlimit.Limit{coreproc.Resource(200): {Soft: 1, Hard: 1}},
			native:  coreproc.CodeUnknownResource,
			foreign: coreproc.CodeUnsupportedPlatform,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := rlimit.Apply(c.pid, c.limits)
		//: keyed on the build-tagged nativeRlimit rather than a
		//: runtime.GOOS == "linux" guess, which wrongly called darwin and the
		//: BSDs unsupported when the service implements them via setrlimit(2).
		want := c.native
		if !nativeRlimit {
			want = c.foreign
		}
		if !errs.HasCode(err, want) {
			t.Fatalf("Apply with %s = %v, want code %v", c.name, err, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// wantCore accepts the core LimitValue type; passing a rlimit.Limit to it
// compiles only when the two are the same type (an alias), giving a static proof
// the facade introduces no distinct named type.
func wantCore(v coreproc.LimitValue) coreproc.LimitValue {
	//: return the value unchanged so the caller can assert on the round-trip.
	return v
}

// TestAliasesIdentical asserts the public names are the very same types and
// constants as the core proc ones, so values cross the facade boundary without
// conversion. A distinct named type here would compile but force every consumer
// into casts the facade exists to spare them.
func TestAliasesIdentical(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		lim  rlimit.Limit
	}
	tests := []tc{
		{"a bounded limit", rlimit.Limit{Soft: 1, Hard: 2}},
		{"the zero limit", rlimit.Limit{}},
		{"an infinite limit", rlimit.Limit{Soft: rlimit.LimitInfinity, Hard: rlimit.LimitInfinity}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: passing the facade alias where the core type is required is the
		//: proof; the value check only guards against a silent conversion.
		core := wantCore(c.lim)
		if core.Soft != c.lim.Soft || core.Hard != c.lim.Hard {
			t.Fatalf("alias round-trip of %s lost data: %+v", c.name, core)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the re-exported constant is a package-level fact, not a per-case one.
	if rlimit.LimitInfinity != coreproc.LimitInfinity {
		t.Fatalf("LimitInfinity = %v, want %v", rlimit.LimitInfinity, coreproc.LimitInfinity)
	}
}
