// Package checks_test — the sdnotify and sdlisten conformance groups.
package checks_test

import (
	"testing"

	"github.com/kitsunium/sdk/e2e/checks"
	"github.com/kitsunium/sdk/e2e/harness"
)

// TestSDNotify pins the sdnotify group's registration.
//
// Supervisor integration is the domain a developer never exercises by hand: the
// checks stand the socket up themselves precisely because nobody runs systemd
// on a laptop, so a check dropped from the group is a path that is proven
// nowhere at all.
func TestSDNotify(t *testing.T) {
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
		//: the unset no-op, the lifecycle accessors, and the readiness round trip.
		{name: "the sdnotify group", build: checks.SDNotify, wantDomain: "sdnotify", minChecks: 3},
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

// TestSDListen pins the sdlisten group's registration, including the empty-set
// check — a foreign LISTEN_PID must yield no sockets and NO error, and that
// distinction is the one a hand-written implementation gets wrong.
func TestSDListen(t *testing.T) {
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
		//: Prepare populating the child's Spec, and the empty set not being an error.
		{name: "the sdlisten group", build: checks.SDListen, wantDomain: "sdlisten", minChecks: 2},
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
