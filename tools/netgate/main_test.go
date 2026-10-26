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

// tightRatios are ten rounds' ratios within 1 % of 1: their sorted 2nd and
// 9th are 0.995 and 1.005.
var tightRatios = []float64{1, 1.003, 0.995, 1.005, 0.998, 1.001, 0.999, 1.01, 1.002, 0.99}

// pairLines writes one paired round per ratio, as BenchmarkLoad's pair
// sub-benchmarks do: A's value 100, B's 100 times the ratio, and the ratio.
func pairLines(load, a, b, unit string, ratios []float64) string {
	var sb strings.Builder
	for _, r := range ratios {
		fmt.Fprintf(&sb, "BenchmarkLoad%s/pair=%s:%s-4  \t  100000\t  1234 ns/op\t 100 a:%s\t %g b:%s\t %g ratio:%s\n",
			load, a, b, unit, 100*r, unit, r, unit)
	}
	return sb.String()
}

// calibrated is every calibration row's rounds within 1 % of 1: a runner a
// step's row may be judged on.
var calibrated = pairLines("L1", "loop", "loop", "cpu-ns/req", tightRatios) +
	pairLines("L2", "handwritten", "handwritten", "cpu-ns/req", tightRatios) +
	pairLines("L3", "recvmmsg", "recvmmsg", "cpu-ns/datagram", tightRatios) +
	pairLines("L3", "recvmmsg", "recvmmsg", "datagrams/syscall", tightRatios)

// scaled multiplies every sample by f.
func scaled(vs []float64, f float64) []float64 {
	out := make([]float64, len(vs))
	for i, v := range vs {
		out[i] = v * f
	}
	return out
}

// TestParseBench reads the pairs' lines and skips everything else: headers,
// other benchmarks, a log line starting with the name, a sub-benchmark of
// the former engine= shape, a pair with an empty or a third side, PASS.
func TestParseBench(t *testing.T) {
	in := "goos: linux\nBenchmarkOther-4 10 5 ns/op\n" +
		"BenchmarkLoadL1/pair=goroutine:loop-4 \t 100 \t 50 ns/op \t 20 a:cpu-ns/req \t 7 b:cpu-ns/req \t 0.35 ratio:cpu-ns/req\n" +
		"BenchmarkLoadL1/pair=goroutine:loop-4 started the child\n" +
		"BenchmarkLoadL1/pair=goroutine:loop 100 51 ns/op 0.4 ratio:cpu-ns/req\n" +
		"BenchmarkLoadL1/engine=loop-4 100 51 ns/op 8 cpu-ns/req\n" +
		"BenchmarkLoadL1/pair=:loop-4 100 51 ns/op 8 ratio:cpu-ns/req\n" +
		"BenchmarkLoadL1/pair=a:b:c-4 100 51 ns/op 8 ratio:cpu-ns/req\n" +
		"BenchmarkLoadL1-4 100 1 ns/op\nBenchmarkLoad/pair=x:y-4 100 1 ns/op\nPASS\n"
	s := mustParse(t, in)
	got := s[seriesKey{Load: "L1", A: "goroutine", B: "loop"}]
	if !slices.Equal(got["ratio:cpu-ns/req"], []float64{0.35, 0.4}) || !slices.Equal(got["a:cpu-ns/req"], []float64{20}) {
		t.Fatalf("series = %v", got)
	}
	if len(s) != 1 {
		t.Fatalf("series read: %v, want only L1 goroutine:loop", s)
	}
}

// TestParseBench_nonFinite drops a NaN or an Inf value like a malformed
// pair: it ends the line's metrics, what came before it stands.
func TestParseBench_nonFinite(t *testing.T) {
	in := "BenchmarkLoadL1/pair=a:b-4 100 50 ns/op NaN ratio:cpu-ns/req\n" +
		"BenchmarkLoadL1/pair=a:b-4 100 50 ns/op 0.7 ratio:cpu-ns/req +Inf lost/op 1 x/op\n" +
		"BenchmarkLoadL1/pair=a:b-4 100 50 ns/op -Inf ratio:cpu-ns/req\n"
	got := mustParse(t, in)[seriesKey{Load: "L1", A: "a", B: "b"}]
	if !slices.Equal(got["ratio:cpu-ns/req"], []float64{0.7}) || len(got["lost/op"]) != 0 || len(got["x/op"]) != 0 {
		t.Fatalf("series = %v, want only the finite ratio 0.7", got)
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
// the bound of its rounds' ratios: a B clearly past it passes, a B whose
// median ratio meets it but whose interval does not fails, and a missing or
// short pair is missing.
func TestEvaluate_thresholds(t *testing.T) {
	cases := []struct {
		name  string
		bench string
		step  string
		want  string
	}{
		{"P6 at -40 % passes", pairLines("L1", "goroutine", "loop", "cpu-ns/req", scaled(tightRatios, 0.6)), "P6", verdictPass},
		{"P6 median at -30 % fails on the bound", pairLines("L1", "goroutine", "loop", "cpu-ns/req", scaled(tightRatios, 0.7)), "P6", verdictFail},
		{"P6 without its pair is missing", pairLines("L1", "loop", "loop", "cpu-ns/req", tightRatios), "P6", verdictMissing},
		{"P4a at 5x and -40 % passes", pairLines("L3", "recvmmsg", "gro", "datagrams/syscall", scaled(tightRatios, 5)) +
			pairLines("L3", "recvmmsg", "gro", "cpu-ns/datagram", scaled(tightRatios, 0.6)), "P4a", verdictPass},
		{"P4a at 4x on the median fails on the lower bound", pairLines("L3", "recvmmsg", "gro", "datagrams/syscall", scaled(tightRatios, 4)) +
			pairLines("L3", "recvmmsg", "gro", "cpu-ns/datagram", scaled(tightRatios, 0.6)), "P4a", verdictFail},
		{"P2 within 5 % passes", pairLines("L2", "handwritten", "chain", "cpu-ns/req", scaled(tightRatios, 1.03)), "P2", verdictPass},
		{"P2 at 5 % on the median fails on the bound", pairLines("L2", "handwritten", "chain", "cpu-ns/req", scaled(tightRatios, 1.05)), "P2", verdictFail},
		// Ratios spread by 25 %: the median 1.0 meets 1.05, the bound 1.1 does not.
		{"P2 equal on the median but noisy fails", pairLines("L2", "handwritten", "chain", "cpu-ns/req", scaled(tenSamples, 0.01)), "P2", verdictFail},
		{"P2 with nine rounds is missing", pairLines("L2", "handwritten", "chain", "cpu-ns/req", tightRatios[:9]), "P2", verdictMissing},
		{"P5 below published passes", pairLines("L1", "published", "loop", "cpu-ns/req", scaled(tightRatios, 0.9)), "P5", verdictPass},
		{"P5 equal on the median fails on the bound", pairLines("L1", "published", "loop", "cpu-ns/req", tightRatios), "P5", verdictFail},
		{"cal-L2 within 1 % passes", pairLines("L2", "handwritten", "handwritten", "cpu-ns/req", tightRatios), "cal-L2", verdictPass},
		{"cal-L2 at 0.93 fails its lower bound", pairLines("L2", "handwritten", "handwritten", "cpu-ns/req", scaled(tightRatios, 0.93)), "cal-L2", verdictFail},
		{"cal-L3 within 25 % passes", pairLines("L3", "recvmmsg", "recvmmsg", "cpu-ns/datagram", scaled(tightRatios, 1.2)) +
			pairLines("L3", "recvmmsg", "recvmmsg", "datagrams/syscall", scaled(tightRatios, 0.85)), "cal-L3", verdictPass},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := evaluate(activeTable(), mustParse(t, c.bench), "abc")
			if g := gateNamed(t, rep, c.step); g.Verdict != c.want {
				t.Fatalf("%s = %s, want %s: %+v", c.step, g.Verdict, c.want, g.Measured)
			}
			if rep.Passed {
				t.Fatal("every gate active and most of them without samples: the report must not pass")
			}
		})
	}
}

// TestEvaluate_pairedCancelsTheRunner is why the measure is paired: two
// engines whose own values swing by 25 % from round to round — the runner,
// not the engines — but whose ratio within each round stays within 1 % pass
// a 5 % gate, which their two series judged apart would fail.
func TestEvaluate_pairedCancelsTheRunner(t *testing.T) {
	var b strings.Builder
	for i, r := range tightRatios {
		a := tenSamples[i]
		fmt.Fprintf(&b, "BenchmarkLoadL2/pair=handwritten:chain-4 100 1 ns/op %g a:cpu-ns/req %g b:cpu-ns/req %g ratio:cpu-ns/req\n", a, a*r, r)
	}
	g := gateNamed(t, evaluate(activeTable(), mustParse(t, b.String()), "abc"), "P2")
	if g.Verdict != verdictPass {
		t.Fatalf("P2 = %s: %+v", g.Verdict, g.Measured)
	}
	m := g.Measured[0]
	if m.A == nil || m.B == nil || m.RatioSummary == nil || m.B.Hi/m.A.Lo <= chainCPUCeiling {
		t.Fatalf("the engines' own intervals should miss the ceiling on their own: %+v", m)
	}
}

// TestEvaluate_shippedTable pins the table as it ships: no step it gates is
// built, so every step's row is inactive and samples that would fail it fail
// nothing, while it still reports what it measured; the calibration rows are
// active, so a run without them, or with one engine unlike itself, fails.
func TestEvaluate_shippedTable(t *testing.T) {
	failing := pairLines("L1", "goroutine", "loop", "cpu-ns/req", scaled(tightRatios, 2))
	rep := evaluate(gates, mustParse(t, calibrated+failing), "abc")
	if !rep.Passed {
		t.Fatalf("calibrated run failed: %+v", rep)
	}
	for _, g := range rep.Gates {
		cal := strings.HasPrefix(g.Step, "cal-")
		if cal != g.Active || (!cal && (g.Verdict != verdictInactive || g.Pending == "")) || (cal && g.Verdict != verdictPass) {
			t.Errorf("%s: active=%t verdict=%s pending=%q", g.Step, g.Active, g.Verdict, g.Pending)
		}
	}
	if g := gateNamed(t, rep, "P6"); g.Measured[0].Verdict != verdictFail {
		t.Fatalf("an inactive gate still reports what it measured: %+v", g.Measured)
	}
	drifted := pairLines("L1", "loop", "loop", "cpu-ns/req", tightRatios) +
		pairLines("L2", "handwritten", "handwritten", "cpu-ns/req", scaled(tightRatios, 1.08)) +
		pairLines("L3", "recvmmsg", "recvmmsg", "cpu-ns/datagram", tightRatios) +
		pairLines("L3", "recvmmsg", "recvmmsg", "datagrams/syscall", tightRatios)
	for _, in := range []string{"", failing, drifted} {
		if evaluate(gates, mustParse(t, in), "abc").Passed {
			t.Fatalf("a run without its calibration, or with one engine 8 %% off itself, passed: %q", in)
		}
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
		"P2":     "L2 handwritten chain cpu-ns/req<=1.05r",
		"P4a":    "L3 recvmmsg gro datagrams/syscall>=4r cpu-ns/datagram<=0.75r",
		"P6":     "L1 goroutine loop cpu-ns/req<=0.7r",
		"P5":     "L1 published loop cpu-ns/req<=1r",
		"cal-L1": "L1 loop loop cpu-ns/req<=1.05r cpu-ns/req>=0.9523809523809523r",
		"cal-L2": "L2 handwritten handwritten cpu-ns/req<=1.05r cpu-ns/req>=0.9523809523809523r",
		"cal-L3": "L3 recvmmsg recvmmsg cpu-ns/datagram<=1.25r cpu-ns/datagram>=0.8r datagrams/syscall<=1.25r datagrams/syscall>=0.8r",
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

// TestJudge_refusesBadRatioAndOp pins the two table errors: a ratio at or
// below zero and an unknown op are no pass.
func TestJudge_refusesBadRatioAndOp(t *testing.T) {
	zero := map[string][]float64{"ratio:m": make([]float64, 10)}
	if m := judge(criterionEntity{Metric: "m", Op: opAtMost, Threshold: 1, Ratio: true}, zero); m.Verdict != verdictMissing {
		t.Errorf("zero ratios: %s", m.Verdict)
	}
	if m := judge(criterionEntity{Metric: "m", Op: "<", Threshold: 1}, map[string][]float64{"b:m": tenSamples}); m.Verdict != verdictFail {
		t.Errorf("unknown op: %s", m.Verdict)
	}
}

// TestRun drives the command: usage errors, the report tied to its commit,
// and the exit status.
func TestRun(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "bench.txt")
	if err := os.WriteFile(in, []byte(calibrated), 0o600); err != nil {
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
		t.Fatalf("calibrated run: %d %s", code, errb.String())
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
	bench := calibrated + pairLines("L1", "goroutine", "loop", "cpu-ns/req", tightRatios)
	if err := os.WriteFile(in, []byte(bench), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"-sha", "abc", "-in", in}, &out, &errb); code != exitGateFailed {
		t.Fatalf("active failing gate: %d\n%s", code, out.String())
	}
}
