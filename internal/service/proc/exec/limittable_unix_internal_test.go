//go:build unix

// Package exec — the Resource→RLIMIT_* table.
package exec

import (
	"maps"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Test_buildResourceLimits pins which resources this platform can set, and that
// no two of them share a constant.
//
// A shared constant is the failure worth guarding: two Resources mapping to one
// RLIMIT_* means a Spec setting both silently applies whichever the map
// iteration reached last, and the loser is a ceiling the caller believes is in
// force. It is exactly the kind of typo a copy-pasted table row produces.
func Test_buildResourceLimits(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		res  coreproc.Resource
	}
	tests := []tc{
		{"open files", coreproc.ResourceNoFile},
		{"core size", coreproc.ResourceCore},
		{"cpu time", coreproc.ResourceCPU},
		{"file size", coreproc.ResourceFSize},
		{"data segment", coreproc.ResourceData},
		{"stack size", coreproc.ResourceStack},
	}
	table := buildResourceLimits()
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if _, ok := table[c.res]; !ok {
			t.Errorf("the table has no entry for %v", c.res)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}

	//: the two resources that live in golang.org/x/sys stay unmapped on
	//: purpose, so a Spec naming them is refused rather than mis-applied.
	for _, absent := range []coreproc.Resource{coreproc.ResourceNProc, coreproc.ResourceMemLock} {
		if _, ok := table[absent]; ok {
			t.Errorf("%v is mapped, but the stdlib exports no constant for it", absent)
		}
	}
	//: the zero value must never resolve, or an unset Resource field would
	//: silently apply a ceiling to whatever RLIMIT_0 happens to be.
	if _, ok := table[coreproc.ResourceUnknown]; ok {
		t.Error("the zero-value Resource resolves to a real RLIMIT constant")
	}
	//: distinct constants, checked on a clone so the walk cannot observe a
	//: concurrent edit.
	seen := make(map[int]coreproc.Resource, len(table))
	for res, rl := range maps.Clone(table) {
		if prior, clash := seen[rl]; clash {
			t.Errorf("%v and %v both map to RLIMIT constant %d", prior, res, rl)
		}
		seen[rl] = res
	}
	//: the package-level table must be the one this function builds.
	if len(resourceLimits) != len(table) {
		t.Errorf("the package table has %d entries, the freshly built one %d",
			len(resourceLimits), len(table))
	}
}
