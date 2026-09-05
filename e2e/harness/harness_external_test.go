// Package harness_test — the conformance runner as the binary drives it.
package harness_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/e2e/harness"
)

// Test constructors pinned here build a Result of each kind; the constructors
// exist so a check cannot invent a status the runner does not know how to tally.

// TestPassed pins the constructor for a behaviour that was exercised and was
// correct — the only outcome that makes a positive claim about the host.
func TestPassed(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// domain, check and detail are what the check reports.
		domain string
		check  string
		detail string
	}
	tests := []tc{
		{name: "a typical pass", domain: "codec", check: "roundtrip/json", detail: "12 bytes round-tripped equal"},
		{name: "a pass with no detail", domain: "errs", check: "code"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := harness.Passed(c.domain, c.check, c.detail)

		if got.Status != harness.Pass {
			t.Fatalf("Status = %v, want PASS", got.Status)
		}
		if got.Domain != c.domain || got.Name != c.check || got.Detail != c.detail {
			t.Fatalf("Result = %+v, want the fields it was given", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFailed pins the ONE outcome that counts against the host. Run returns the
// Fail count as the process exit code, so a constructor that produced anything
// else here would make a broken kernel exit zero.
func TestFailed(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// domain, check and detail are what the check reports.
		domain string
		check  string
		detail string
	}
	tests := []tc{
		{name: "a typical failure", domain: "cgroup", check: "placement", detail: "pid absent from cgroup.procs"},
		{name: "a failure with no detail", domain: "signal", check: "relay"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := harness.Failed(c.domain, c.check, c.detail)

		if got.Status != harness.Fail {
			t.Fatalf("Status = %v, want FAIL — only this outcome counts against the host",
				got.Status)
		}
		if got.Domain != c.domain || got.Name != c.check || got.Detail != c.detail {
			t.Fatalf("Result = %+v, want the fields it was given", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestNotSupported pins the uniform off-platform contract, which is a SUCCESS.
//
// The whole point of the conformance run is that where a platform has no native
// mechanic the public API degrades uniformly rather than misbehaving. A
// constructor that produced a Fail here would make every non-Linux host red for
// behaving exactly as documented.
func TestNotSupported(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// domain, check and detail are what the check reports.
		domain string
		check  string
		detail string
	}
	tests := []tc{
		{name: "an off-platform cgroup", domain: "cgroup", check: "conformance", detail: "not Linux"},
		{name: "an off-platform reaper", domain: "reaper", check: "subreaper", detail: "no prctl equivalent"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := harness.NotSupported(c.domain, c.check, c.detail)

		//: not a failure: the platform behaved exactly as documented.
		if got.Status != harness.Unsupported {
			t.Fatalf("Status = %v, want UNSUPPORTED", got.Status)
		}
		if got.Status == harness.Fail {
			t.Fatal("an expected off-platform degradation was counted against the host")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestSkipped pins the outcome that makes NO claim either way — an environmental
// gap such as a missing shell or an undelegated cgroup hierarchy. It is
// deliberately distinct from Unsupported: one says the platform is fine, the
// other says we could not find out.
func TestSkipped(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// domain, check and detail are what the check reports.
		domain string
		check  string
		detail string
	}
	tests := []tc{
		{name: "no shell", domain: "process", check: "exit-code", detail: "/bin/sh absent"},
		{name: "no cgroup delegation", domain: "cgroup", check: "placement", detail: "Create: permission denied"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := harness.Skipped(c.domain, c.check, c.detail)

		if got.Status != harness.Skip {
			t.Fatalf("Status = %v, want SKIP", got.Status)
		}
		//: distinct from Unsupported: one says the platform is fine, the other
		//: says we could not find out.
		if got.Status == harness.Unsupported {
			t.Fatal("an environmental gap was reported as an off-platform success")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestRun pins the runner's contract end to end: every check runs, the table is
// ordered deterministically, and the return value is the FAIL COUNT alone.
//
// The count is the process exit code, so counting an Unsupported or a Skip would
// turn every platform without a mechanic red — and missing a Fail would let a
// broken kernel ship.
func TestRun(t *testing.T) {
	t.Parallel()
	result := func(domain, name string, status harness.Status) harness.Check {
		return func() harness.Result {
			return harness.Result{Domain: domain, Name: name, Status: status, Detail: "d"}
		}
	}

	type tc struct {
		// name describes the case.
		name string
		// groups are what the binary registers.
		groups []harness.CheckGroup
		// wantFails is the exit code the run must produce.
		wantFails int
		// wantRows is how many table rows must be printed.
		wantRows int
	}
	tests := []tc{
		{name: "nothing registered"},
		{
			name: "a clean host",
			groups: []harness.CheckGroup{{Domain: "codec", Checks: []harness.Check{
				result("codec", "a", harness.Pass),
				result("codec", "b", harness.Pass),
			}}},
			wantRows: 2,
		},
		{
			//: neither an off-platform degradation nor an environmental gap is a
			//: failure of the SDK.
			name: "a platform without the mechanic",
			groups: []harness.CheckGroup{{Domain: "cgroup", Checks: []harness.Check{
				result("cgroup", "a", harness.Unsupported),
				result("cgroup", "b", harness.Skip),
			}}},
			wantRows: 2,
		},
		{
			name: "one real failure",
			groups: []harness.CheckGroup{{Domain: "proc", Checks: []harness.Check{
				result("proc", "a", harness.Pass),
				result("proc", "b", harness.Fail),
				result("proc", "c", harness.Unsupported),
			}}},
			wantFails: 1, wantRows: 3,
		},
		{
			name: "failures across domains",
			groups: []harness.CheckGroup{
				{Domain: "proc", Checks: []harness.Check{result("proc", "a", harness.Fail)}},
				{Domain: "codec", Checks: []harness.Check{result("codec", "a", harness.Fail)}},
			},
			wantFails: 2, wantRows: 2,
		},
		{
			//: a panicking check is contained and recorded as a failure, so one
			//: misbehaving check cannot take the run down.
			name: "a check that panics",
			groups: []harness.CheckGroup{{Domain: "proc", Checks: []harness.Check{
				func() harness.Result { panic("check exploded") },
				result("proc", "b", harness.Pass),
			}}},
			wantFails: 1, wantRows: 2,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var out bytes.Buffer

		fails := harness.Run(&out, c.groups)

		if fails != c.wantFails {
			t.Fatalf("Run = %d fails, want %d — the count is the process exit code",
				fails, c.wantFails)
		}
		//: one row per check plus a blank line and the summary.
		rows := strings.Count(out.String(), "\n") - 2
		if rows != c.wantRows {
			t.Fatalf("the table has %d rows, want %d:\n%s", rows, c.wantRows, out.String())
		}
		if !strings.Contains(out.String(), "summary:") {
			t.Errorf("the report has no summary line:\n%s", out.String())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestRun_OrdersTheTable pins the stable ordering.
//
// The conformance table is read by diffing one machine's run against another's,
// so a table whose row order depends on registration order — or on map iteration
// anywhere upstream — makes every such diff unreadable even when both hosts
// agree.
func TestRun_OrdersTheTable(t *testing.T) {
	t.Parallel()
	row := func(domain, name string) harness.Check {
		return func() harness.Result {
			return harness.Result{Domain: domain, Name: name, Status: harness.Pass, Detail: "d"}
		}
	}

	type tc struct {
		// name describes the case.
		name string
		// groups are registered in a deliberately unsorted order.
		groups []harness.CheckGroup
		// want is the domain/name order the table must print.
		want []string
	}
	tests := []tc{
		{
			name: "domains out of order",
			groups: []harness.CheckGroup{
				{Domain: "proc", Checks: []harness.Check{row("proc", "a")}},
				{Domain: "codec", Checks: []harness.Check{row("codec", "a")}},
			},
			want: []string{"codec a", "proc a"},
		},
		{
			name: "checks out of order within one domain",
			groups: []harness.CheckGroup{{Domain: "codec", Checks: []harness.Check{
				row("codec", "zeta"), row("codec", "alpha"), row("codec", "mid"),
			}}},
			want: []string{"codec alpha", "codec mid", "codec zeta"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var out bytes.Buffer

		harness.Run(&out, c.groups)

		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		for i, want := range c.want {
			parts := strings.Fields(lines[i])
			//: domain, status, name — the status sits between the two keys.
			if len(parts) < 3 {
				t.Fatalf("row %d is not a table row: %q", i, lines[i])
			}
			if got := parts[0] + " " + parts[2]; got != want {
				t.Fatalf("row %d is %q, want %q — a table whose order depends on "+
					"registration cannot be diffed across machines", i, got, want)
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
