// Package main — the conformance binary's registration.
package main

import (
	"testing"
)

// Test_conformanceGroups pins the REGISTRATION, which is the one thing about a
// conformance run that cannot be seen from its own output.
//
// A domain whose group is built and then not listed here contributes no rows —
// and a table with no cgroup rows reads as "cgroups were not applicable on this
// host", which is exactly what a legitimately off-platform run looks like. The
// same is true of a group registered twice: its rows appear twice and the
// summary's counts stop matching the number of behaviours actually exercised.
func Test_conformanceGroups(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// wantDomains are the domains that must be registered.
		wantDomains []string
	}
	tests := []tc{
		{
			name: "every domain the SDK ships",
			wantDomains: []string{
				"codec", "crypto", "logger", "errs",
				"process", "rlimit", "signal",
				"cgroup", "reaper", "sdnotify", "sdlisten",
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		groups := conformanceGroups()

		seen := make(map[string]int, len(groups))
		for i, group := range groups {
			//: a group with no domain produces rows nobody can attribute.
			if group.Domain == "" {
				t.Fatalf("group %d registers no domain", i)
			}
			//: a group with no checks is a domain that looks covered and is not.
			if len(group.Checks) == 0 {
				t.Fatalf("group %q registers no checks", group.Domain)
			}
			//: a nil check panics inside the runner, where it is recorded as a
			//: failure of "panic" rather than of the behaviour it stood for.
			for j, check := range group.Checks {
				if check == nil {
					t.Fatalf("group %q check %d is nil", group.Domain, j)
				}
			}
			seen[group.Domain]++
		}
		for _, want := range c.wantDomains {
			//: a missing domain is invisible in the table: no rows reads exactly
			//: like a legitimately off-platform run.
			if seen[want] == 0 {
				t.Errorf("domain %q is not registered — its absence is invisible in "+
					"the conformance table", want)
			}
			//: a domain registered twice prints its rows twice and makes the
			//: summary's counts disagree with the behaviours exercised.
			if seen[want] > 1 {
				t.Errorf("domain %q is registered %d times", want, seen[want])
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
