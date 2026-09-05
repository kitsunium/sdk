// Package checks_test — the cgroup and reaper conformance groups.
package checks_test

import (
	"testing"

	"github.com/kitsunium/sdk/e2e/checks"
	"github.com/kitsunium/sdk/e2e/harness"
)

// TestCgroup pins the cgroup group's registration.
//
// Confinement is the domain where a silent failure is most expensive: a child
// that runs OUTSIDE its group looks identical to one inside it until the limit
// that was supposed to hold does not. Both the limit read-back and the
// placement-at-exec check have to be registered, because they fail
// independently.
func TestCgroup(t *testing.T) {
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
		//: the limit round trip and the placement-at-exec proof.
		{name: "the cgroup group", build: checks.Cgroup, wantDomain: "cgroup", minChecks: 2},
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

// TestReaper pins the reaper group's registration. Subreaper adoption is
// unobservable from inside the process that needs it, so the only way to know it
// works on a kernel is to orphan a grandchild there and watch it come home.
func TestReaper(t *testing.T) {
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
		//: arming the subreaper, the pid-1 probe, construction, and adoption.
		{name: "the reaper group", build: checks.Reaper, wantDomain: "reaper", minChecks: 4},
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
