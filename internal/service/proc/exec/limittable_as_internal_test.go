//go:build unix && !openbsd

// Package exec — the address-space limit mapping.
package exec

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Test_addPlatformLimits pins that RLIMIT_AS is added where the kernel has it.
//
// The split exists because OpenBSD has no address-space rlimit at all, and the
// alternative — referencing syscall.RLIMIT_AS from the shared table — would
// simply fail to compile there. This half must therefore ADD the entry, and it
// must add it without disturbing what the caller already put in the map.
func Test_addPlatformLimits(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: what the map already holds when the platform half runs.
		seed map[coreproc.Resource]int
	}
	tests := []tc{
		{"an empty table", map[coreproc.Resource]int{}},
		{"a table with one entry", map[coreproc.Resource]int{coreproc.ResourceNoFile: 7}},
		{
			"a table that already names the address space",
			map[coreproc.Resource]int{coreproc.ResourceAS: -1},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		before := len(c.seed)
		_, hadAS := c.seed[coreproc.ResourceAS]

		addPlatformLimits(c.seed)

		//: the address-space limit must now resolve.
		if _, ok := c.seed[coreproc.ResourceAS]; !ok {
			t.Fatal("addPlatformLimits did not map the address-space limit")
		}
		//: nothing else may have been added or removed.
		want := before
		if !hadAS {
			want++
		}
		if len(c.seed) != want {
			t.Errorf("the table has %d entries, want %d", len(c.seed), want)
		}
		//: a pre-existing unrelated entry survives.
		if v, ok := c.seed[coreproc.ResourceNoFile]; ok && v != 7 {
			t.Errorf("the existing NoFile entry became %d", v)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
