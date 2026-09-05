//go:build unix

// Package exec — the pre-spawn rlimit validation.
package exec

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_checkLimits pins the SDK's no-silent-field-loss rule at the spawn
// boundary.
//
// The rule matters because the alternative is invisible: a Spec naming a
// resource this platform cannot set would otherwise spawn a process WITHOUT the
// ceiling that was asked for, and nothing downstream can tell that apart from a
// process that simply never hit it. Rejecting before the fork is what makes the
// gap a startup error instead of a production surprise.
func Test_checkLimits(t *testing.T) {
	t.Parallel()
	umask := 0o027

	type tc struct {
		name    string
		spec    coreproc.Spec
		wantErr bool
	}
	tests := []tc{
		{name: "a spec with no limits at all"},
		{
			name: "an empty limit map",
			spec: coreproc.Spec{Rlimits: map[coreproc.Resource]coreproc.LimitValue{}},
		},
		{
			//: a umask is honoured through the trampoline, so it is not a
			//: reason to refuse the spawn.
			name: "a umask alone",
			spec: coreproc.Spec{Umask: &umask},
		},
		{
			name: "every mappable resource",
			spec: coreproc.Spec{Rlimits: everyMappedLimit()},
		},
		{
			name: "the zero-value resource",
			spec: coreproc.Spec{Rlimits: map[coreproc.Resource]coreproc.LimitValue{
				coreproc.ResourceUnknown: {Soft: 1, Hard: 1},
			}},
			wantErr: true,
		},
		{
			//: NPROC and MEMLOCK live in golang.org/x/sys, which this module
			//: does not depend on, so they are deliberately unmapped.
			name: "a resource the stdlib does not expose",
			spec: coreproc.Spec{Rlimits: map[coreproc.Resource]coreproc.LimitValue{
				coreproc.ResourceNProc: {Soft: 1, Hard: 1},
			}},
			wantErr: true,
		},
		{
			//: one unmappable entry among mappable ones must still refuse; a
			//: partial application is the exact failure the rule prevents.
			name: "an unmapped resource beside mapped ones",
			spec: coreproc.Spec{Rlimits: map[coreproc.Resource]coreproc.LimitValue{
				coreproc.ResourceNoFile:  {Soft: 1024, Hard: 1024},
				coreproc.ResourceUnknown: {Soft: 1, Hard: 1},
			}},
			wantErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := checkLimits(c.spec)
		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeUnknownResource) {
				t.Fatalf("checkLimits(%s) = %v, want UNKNOWN_RESOURCE", c.name, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("checkLimits(%s) = %v, want nil", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// everyMappedLimit builds a Spec limit set naming every resource the platform
// table maps, so a resource added to the table cannot slip past the validator.
func everyMappedLimit() map[coreproc.Resource]coreproc.LimitValue {
	out := make(map[coreproc.Resource]coreproc.LimitValue, len(resourceLimits))
	for res := range resourceLimits {
		out[res] = coreproc.LimitValue{Soft: 1, Hard: 1}
	}
	return out
}
