// Package checks_test — the process, rlimit and signal conformance groups.
package checks_test

import (
	"testing"

	"github.com/kitsunium/sdk/e2e/checks"
	"github.com/kitsunium/sdk/e2e/harness"
)

// TestProcess pins the process group's registration.
//
// Spawning is the domain where "it compiles" says least: exit codes, stdio
// capture and group-aware stop are all kernel behaviour, and a check dropped
// from the group is a behaviour nobody proves on the platform under test.
func TestProcess(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// build is the group under test.
		build func() harness.CheckGroup
		// wantDomain is the label every Result must carry.
		wantDomain string
		// minChecks is the smallest number of checks the group may register.
		minChecks int
	}
	tests := []tc{
		//: exit code, stdio capture, group-aware stop.
		{name: "the process group", build: checks.Process, wantDomain: "process", minChecks: 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		group := c.build()

		assertGroup(t, group, c.wantDomain)

		if len(group.Checks) < c.minChecks {
			t.Fatalf("the group registers %d checks, want at least %d",
				len(group.Checks), c.minChecks)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestRlimit pins the rlimit group's registration. Each of its checks proves the
// re-exec trampoline actually ran a syscall in the fresh child — the one thing
// that cannot be observed from the parent at all.
func TestRlimit(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// build is the group under test.
		build func() harness.CheckGroup
		// wantDomain is the label every Result must carry.
		wantDomain string
		// minChecks is the smallest number of checks the group may register.
		minChecks int
	}
	tests := []tc{
		//: the open-file ceiling, the umask, and the core-dump ceiling.
		{name: "the rlimit group", build: checks.Rlimit, wantDomain: "rlimit", minChecks: 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		group := c.build()

		assertGroup(t, group, c.wantDomain)

		if len(group.Checks) < c.minChecks {
			t.Fatalf("the group registers %d checks, want at least %d",
				len(group.Checks), c.minChecks)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestSignal pins the signal group's registration.
func TestSignal(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// build is the group under test.
		build func() harness.CheckGroup
		// wantDomain is the label every Result must carry.
		wantDomain string
		// minChecks is the smallest number of checks the group may register.
		minChecks int
	}
	tests := []tc{
		//: the name/number round trip and Relay's termination contract.
		{name: "the signal group", build: checks.Signal, wantDomain: "signal", minChecks: 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		group := c.build()

		assertGroup(t, group, c.wantDomain)

		if len(group.Checks) < c.minChecks {
			t.Fatalf("the group registers %d checks, want at least %d",
				len(group.Checks), c.minChecks)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
