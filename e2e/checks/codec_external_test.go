// Package checks_test — the conformance groups as the binary registers them.
package checks_test

import (
	"testing"

	"github.com/kitsunium/sdk/e2e/checks"
	"github.com/kitsunium/sdk/e2e/harness"
)

// assertGroup pins the three ways a registered group can silently contribute
// nothing to the conformance table.
//
// A group with no domain produces rows nobody can attribute. A group with no
// checks is a domain that looks covered and is not — the worst of the three,
// because the table shows nothing missing. And a nil Check panics inside the
// runner, which recovers it as a Fail attributed to "panic" rather than to the
// behaviour that was supposed to be exercised.
func assertGroup(t *testing.T, group harness.CheckGroup, wantDomain string) {
	t.Helper()
	if group.Domain != wantDomain {
		t.Fatalf("Domain = %q, want %q", group.Domain, wantDomain)
	}
	if len(group.Checks) == 0 {
		t.Fatal("the group registers no checks — the domain looks covered and is not")
	}
	for i, check := range group.Checks {
		//: a nil check reaches the runner and panics there, where it is recorded
		//: as a failure of "panic" rather than of the behaviour it stood for.
		if check == nil {
			t.Fatalf("check %d is nil", i)
		}
	}
}

// TestCodec pins the codec group's registration. Every format the SDK ships has
// to be exercised on the host, so a format dropped from the group is a codec
// nobody proves works there.
func TestCodec(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// wantDomain is the label every Result must carry.
		wantDomain string
		// minChecks is the smallest number of checks the group may register.
		minChecks int
	}
	tests := []tc{
		//: one round trip per registered format, plus the registry probe.
		{name: "the codec group", wantDomain: "codec", minChecks: 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		group := checks.Codec()

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
