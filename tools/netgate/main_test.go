package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// tenSamples is a set of ten whose sorted 2nd and 9th are 97 and 110:
// benchstat prints it as 100.5 ± 9 % (measured with benchstat itself).
var tenSamples = []float64{100, 103, 97, 110, 95, 101, 99, 120, 102, 98}

// TestSummarize_matchesBenchstat pins the interval to benchstat's order
// statistics: at n = 10 and 95 % the 2nd and 9th sorted samples.
func TestSummarize_matchesBenchstat(t *testing.T) {
	s, err := summarize(tenSamples, confidence)
	if err != nil {
		t.Fatal(err)
	}
	if s.N != 10 || s.Lo != 97 || s.Median != 100.5 || s.Hi != 110 {
		t.Fatalf("summary = %+v, want n=10 lo=97 median=100.5 hi=110", s)
	}
}

// TestSummarize_refusesFewSamples pins the plan's n as a floor.
func TestSummarize_refusesFewSamples(t *testing.T) {
	if _, err := summarize(tenSamples[:9], confidence); !errors.Is(err, errTooFewSamples) {
		t.Fatalf("nine samples: err = %v, want errTooFewSamples", err)
	}
}

// TestOrderStatistic pins k against the binomial tails: n = 10 gives 2
// (P(X<=1) = 11/1024, P(X<=2) = 56/1024 > 2.5 %), n = 20 gives 6, and a
// level no order statistic reaches gives 0.
func TestOrderStatistic(t *testing.T) {
	for _, c := range []struct {
		n    int
		tail float64
		want int
	}{{10, 0.025, 2}, {20, 0.025, 6}, {10, 0.0001, 0}, {5, 0.025, 0}} {
		if got := orderStatistic(c.n, c.tail); got != c.want {
			t.Errorf("orderStatistic(%d, %g) = %d, want %d", c.n, c.tail, got, c.want)
		}
	}
}

// benchLines writes one result line per value, as `go test -bench` does.
func benchLines(load, engine, unit string, vs []float64) string {
	var b strings.Builder
	for _, v := range vs {
		fmt.Fprintf(&b, "BenchmarkLoad%s/engine=%s-4  \t  100000\t  1234 ns/op\t %g %s\n", load, engine, v, unit)
	}
	return b.String()
}

// scaled multiplies every sample by f.
func scaled(vs []float64, f float64) []float64 {
	out := make([]float64, len(vs))
	for i, v := range vs {
		out[i] = v * f
	}
	return out
}

// TestParseBench reads the loads' lines and skips everything else: headers,
// other benchmarks, a log line starting with the name, PASS.
func TestParseBench(t *testing.T) {
	in := "goos: linux\nBenchmarkOther-4 10 5 ns/op\n" +
		"BenchmarkLoadL1/engine=loop-4 \t 100 \t 50 ns/op \t 7.5 cpu-ns/req \t 0.5 lost/op\n" +
		"BenchmarkLoadL1/engine=loop-4 started the child\n" +
		"BenchmarkLoadL1/engine=loop 100 51 ns/op 8 cpu-ns/req\n" +
		"BenchmarkLoadL1-4 100 1 ns/op\nBenchmarkLoad/engine=x-4 100 1 ns/op\nPASS\n"
	s, err := parseBench(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	got := s[seriesKey{Load: "L1", Engine: "loop"}]
	if !slices.Equal(got["cpu-ns/req"], []float64{7.5, 8}) || !slices.Equal(got["lost/op"], []float64{0.5}) {
		t.Fatalf("series = %v", got)
	}
	if len(s) != 1 {
		t.Fatalf("series read: %v, want only L1/loop", s)
	}
}

// TestParseBench_nonFinite drops a NaN or an Inf value like a malformed
// pair: it ends the line's metrics, what came before it stands.
func TestParseBench_nonFinite(t *testing.T) {
	in := "BenchmarkLoadL1/engine=loop-4 100 50 ns/op NaN cpu-ns/req\n" +
		"BenchmarkLoadL1/engine=loop-4 100 50 ns/op 7 cpu-ns/req +Inf lost/op 1 x/op\n" +
		"BenchmarkLoadL1/engine=loop-4 100 50 ns/op -Inf cpu-ns/req\n"
	got := mustParse(t, in)[seriesKey{Load: "L1", Engine: "loop"}]
	if !slices.Equal(got["cpu-ns/req"], []float64{7}) || len(got["lost/op"]) != 0 || len(got["x/op"]) != 0 {
		t.Fatalf("series = %v, want only the finite cpu-ns/req 7", got)
	}
}

// activeTable is the plan's table with every gate turned on, so each
// threshold is exercised as it will be once its step lands.
func activeTable() []gateEntity {
	t := slices.Clone(gates)
	for i := range t {
		t[i].Active = true
	}
	return t
}

// gateNamed returns the gate of step from rep.
func gateNamed(t *testing.T, rep *reportEntity, step string) gateResult {
	t.Helper()
	for _, g := range rep.Gates {
		if g.Step == step {
			return g
		}
	}
	t.Fatalf("no gate %s in the report", step)
	return gateResult{}
}

// TestEvaluate_thresholds holds each gate of the table to its threshold on
// the interval's bound: a B clearly past it passes, a B whose median meets
// it but whose interval does not fails, and a missing side fails an active
// gate.
func TestEvaluate_thresholds(t *testing.T) {
	cases := []struct {
		name  string
		bench string
		step  string
		want  string
	}{
		{"P6 at -40 % passes", benchLines("L1", "goroutine", "cpu-ns/req", tenSamples) + benchLines("L1", "loop", "cpu-ns/req", scaled(tenSamples, 0.6)), "P6", verdictPass},
		// Median ratio 0.70, but the upper bound 110*0.7/97 is 0.79.
		{"P6 median at -30 % fails on the bound", benchLines("L1", "goroutine", "cpu-ns/req", tenSamples) + benchLines("L1", "loop", "cpu-ns/req", scaled(tenSamples, 0.7)), "P6", verdictFail},
		{"P6 without A is missing", benchLines("L1", "loop", "cpu-ns/req", tenSamples), "P6", verdictMissing},
		{"P4a at 5x and -40 % passes", benchLines("L3", "recvmmsg", "datagrams/syscall", tenSamples) + benchLines("L3", "gro", "datagrams/syscall", scaled(tenSamples, 5)) +
			benchLines("L3", "recvmmsg", "cpu-ns/datagram", tenSamples) + benchLines("L3", "gro", "cpu-ns/datagram", scaled(tenSamples, 0.5)), "P4a", verdictPass},
		{"P4a at 4x on the median fails on the lower bound", benchLines("L3", "recvmmsg", "datagrams/syscall", tenSamples) + benchLines("L3", "gro", "datagrams/syscall", scaled(tenSamples, 4)) +
			benchLines("L3", "recvmmsg", "cpu-ns/datagram", tenSamples) + benchLines("L3", "gro", "cpu-ns/datagram", scaled(tenSamples, 0.5)), "P4a", verdictFail},
		{"P2 within 5 % passes", benchLines("L2", "handwritten", "cpu-ns/req", scaled(tenSamples, 1.2)) + benchLines("L2", "chain", "cpu-ns/req", tenSamples), "P2", verdictPass},
		// Median ratio 1.0, upper bound 110/97 = 1.13.
		{"P2 equal on the median fails on the bound", benchLines("L2", "handwritten", "cpu-ns/req", tenSamples) + benchLines("L2", "chain", "cpu-ns/req", tenSamples), "P2", verdictFail},
		{"P2 with nine runs is missing", benchLines("L2", "handwritten", "cpu-ns/req", tenSamples) + benchLines("L2", "chain", "cpu-ns/req", tenSamples[:9]), "P2", verdictMissing},
		{"P5 no worse than published passes", benchLines("L1", "published", "cpu-ns/req", scaled(tenSamples, 1.3)) + benchLines("L1", "loop", "cpu-ns/req", tenSamples), "P5", verdictPass},
		// Median ratio 1.0, upper bound 110/97 = 1.13.
		{"P5 equal on the median fails on the bound", benchLines("L1", "published", "cpu-ns/req", tenSamples) + benchLines("L1", "loop", "cpu-ns/req", tenSamples), "P5", verdictFail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := parseBench(strings.NewReader(c.bench))
			if err != nil {
				t.Fatal(err)
			}
			rep := evaluate(activeTable(), s, "abc")
			if g := gateNamed(t, rep, c.step); g.Verdict != c.want {
				t.Fatalf("%s = %s, want %s: %+v", c.step, g.Verdict, c.want, g.Measured)
			}
			if rep.Passed {
				t.Fatal("every gate active and most of them without samples: the report must not pass")
			}
		})
	}
}

// TestEvaluate_inactiveNeverFails pins the table as it ships: no step it
// gates is built, so no gate is active, and a run with no sample at all, or
// with samples that would fail, passes while reporting each gate inactive.
func TestEvaluate_inactiveNeverFails(t *testing.T) {
	failing := benchLines("L1", "goroutine", "cpu-ns/req", tenSamples) + benchLines("L1", "loop", "cpu-ns/req", scaled(tenSamples, 2))
	for _, in := range []string{"", failing} {
		s, err := parseBench(strings.NewReader(in))
		if err != nil {
			t.Fatal(err)
		}
		rep := evaluate(gates, s, "abc")
		if !rep.Passed {
			t.Fatalf("inactive table failed: %+v", rep)
		}
		for _, g := range rep.Gates {
			if g.Active || g.Verdict != verdictInactive || g.Pending == "" {
				t.Errorf("%s: active=%t verdict=%s pending=%q, want inactive with what it waits for", g.Step, g.Active, g.Verdict, g.Pending)
			}
		}
	}
	if g := gateNamed(t, evaluate(gates, mustParse(t, failing), "abc"), "P6"); g.Measured[0].Verdict != verdictFail {
		t.Fatalf("an inactive gate still reports what it measured: %+v", g.Measured)
	}
}

// mustParse parses in or fails the test.
func mustParse(t *testing.T, in string) samplesEntity {
	t.Helper()
	s, err := parseBench(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestGates_table pins the plan's table: the steps, their loads and engines,
// one metric and one threshold per criterion, the 0-alloc exclusions named,
// and only known loads, metrics and ops.
func TestGates_table(t *testing.T) {
	want := map[string]string{
		"P2":  "L2 handwritten chain cpu-ns/req<=1.05r",
		"P4a": "L3 recvmmsg gro datagrams/syscall>=4r cpu-ns/datagram<=0.75r",
		"P6":  "L1 goroutine loop cpu-ns/req<=0.7r",
		"P5":  "L1 published loop cpu-ns/req<=1r",
	}
	if len(gates) != len(want) {
		t.Fatalf("%d gates, want %d", len(gates), len(want))
	}
	for _, g := range gates {
		got := g.Load + " " + g.A + " " + g.B
		for _, c := range g.Criteria {
			got += fmt.Sprintf(" %s%s%g", c.Metric, c.Op, c.Threshold)
			if c.Ratio {
				got += "r"
			}
			if !slices.Contains([]string{opAtMost, opAtLeast}, c.Op) {
				t.Errorf("%s: op %q", g.Step, c.Op)
			}
		}
		if got != want[g.Step] {
			t.Errorf("%s = %q, want %q", g.Step, got, want[g.Step])
		}
		if !slices.Contains([]string{loadL1, loadL2, loadL3}, g.Load) {
			t.Errorf("%s: unknown load %q", g.Step, g.Load)
		}
	}
	if !slices.Equal(allocExclusions, []string{"full queue", "setup/Start", "error paths"}) {
		t.Errorf("alloc exclusions = %v", allocExclusions)
	}
	if gates[0].Step != "P2" || !strings.Contains(gates[0].AllocBudget, "0 allocations") {
		t.Errorf("P2's 0-alloc budget is not named: %q", gates[0].AllocBudget)
	}
}

// TestJudge_refusesBadReferenceAndOp pins the two table errors: a reference
// interval at zero and an unknown op are no pass.
func TestJudge_refusesBadReferenceAndOp(t *testing.T) {
	zero := make([]float64, 10)
	if m := judge(criterionEntity{Metric: "m", Op: opAtMost, Threshold: 1, Ratio: true}, zero, tenSamples); m.Verdict != verdictMissing {
		t.Errorf("zero reference: %s", m.Verdict)
	}
	if m := judge(criterionEntity{Metric: "m", Op: "<", Threshold: 1}, nil, tenSamples); m.Verdict != verdictFail {
		t.Errorf("unknown op: %s", m.Verdict)
	}
}

// TestRun drives the command: usage errors, the report tied to its commit,
// and the exit status.
func TestRun(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "bench.txt")
	if err := os.WriteFile(in, []byte(benchLines("L1", "loop", "cpu-ns/req", tenSamples)), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"-in", in}, &out, &errb); code != exitUsage || !strings.Contains(errb.String(), "-sha") {
		t.Fatalf("no -sha: %d %q", code, errb.String())
	}
	if code := run([]string{"-sha", "abc"}, &out, &errb); code != exitUsage {
		t.Fatalf("no -in: %d", code)
	}
	if code := run([]string{"-sha", "abc", "-in", filepath.Join(dir, "none")}, &out, &errb); code != exitUsage {
		t.Fatalf("unreadable -in: %d", code)
	}
	report := filepath.Join(dir, "report.json")
	out.Reset()
	if code := run([]string{"-sha", "abc123", "-in", in, "-out", report}, &out, &errb); code != 0 {
		t.Fatalf("inactive table: %d %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "commit abc123") || !strings.Contains(out.String(), "P6   inactive") {
		t.Fatalf("summary:\n%s", out.String())
	}
	b, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var rep reportEntity
	if err := json.Unmarshal(b, &rep); err != nil || rep.SHA != "abc123" || !rep.Passed || len(rep.Gates) != len(gates) {
		t.Fatalf("report: %v %+v", err, rep)
	}
	out.Reset()
	if code := run([]string{"-list"}, &out, &errb); code != 0 || !strings.Contains(out.String(), `"step": "P4a"`) {
		t.Fatalf("-list: %d\n%s", code, out.String())
	}
	if code := run([]string{"-bogus"}, &out, &errb); code != exitUsage {
		t.Fatalf("bad flag: %d", code)
	}
}

// TestRun_activeGateFails pins exit 1: with a gate turned on and its samples
// failing, the run fails.
func TestRun_activeGateFails(t *testing.T) {
	saved := gates
	t.Cleanup(func() { gates = saved })
	gates = activeTable()
	in := filepath.Join(t.TempDir(), "bench.txt")
	bench := benchLines("L1", "goroutine", "cpu-ns/req", tenSamples) + benchLines("L1", "loop", "cpu-ns/req", tenSamples)
	if err := os.WriteFile(in, []byte(bench), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"-sha", "abc", "-in", in}, &out, &errb); code != exitGateFailed {
		t.Fatalf("active failing gate: %d\n%s", code, out.String())
	}
}
