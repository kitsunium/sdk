// Package checks_test — the logger and errs conformance groups.
package checks_test

import (
	"testing"

	"github.com/kitsunium/sdk/e2e/checks"
	"github.com/kitsunium/sdk/e2e/harness"
)

// TestLogger pins the logger group's registration.
//
// The logger is the one domain whose failure is silent by construction: a
// logger that writes nothing, or writes to the wrong stream, looks exactly like
// a quiet run. The group is what turns that into a row.
func TestLogger(t *testing.T) {
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
		//: text output, attributes, the framework version, the level filter, the
		//: builder and the default logger.
		{name: "the logger group", build: checks.Logger, wantDomain: "logger", minChecks: 6},
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

// TestErrs pins the errs group's registration. The error model is what every
// other domain reports through, so a gap here makes every other row less
// trustworthy rather than merely leaving one domain uncovered.
func TestErrs(t *testing.T) {
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
		//: the code, the reason, the public/private split, the two matchers, the
		//: octet decomposition, the exit/HTTP mapping and the nil degradation.
		{name: "the errs group", build: checks.Errs, wantDomain: "errs", minChecks: 8},
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
