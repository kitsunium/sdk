// Package main — the gate table: the merge gates of the network plan (P8, R7:
// a gate is placed at the merge, never at the release), written as data. One
// row per step, each comparing engine B against engine A on one load,
// measured in the same job on the same runner. A criterion is one metric and
// one threshold, judged on the bound of the confidence interval that can fail
// it (stats.go, eval.go).
package main

const (
	// opAtMost passes when the interval's UPPER bound is at or below the
	// threshold: the worst the samples allow still meets it.
	opAtMost string = "<="
	// opAtLeast passes when the interval's LOWER bound is at or above it.
	opAtLeast string = ">="

	// loadL1 is a plaintext GET with a 1 KiB body over 1000 keep-alive
	// connections. The loads are fixed in the bench code of
	// internal/service/net/server (load_gate_linux_test.go,
	// load_l3_linux_internal_test.go): the gate names them, the bench defines
	// them.
	loadL1 string = "L1"
	// loadL2 is L1 with a Bearer token and routing.
	loadL2 string = "L2"
	// loadL3 is UDP, 1200-byte datagrams from a GSO sender of 54 segments, 4
	// flows at a fixed rate, over a veth pair.
	loadL3 string = "L3"

	// metricCPUPerReq is the server process's user+system CPU per request
	// (testing.B.ReportMetric units, as the benches report them).
	metricCPUPerReq string = "cpu-ns/req"
	// metricCPUPerDatagram is the receiver's user+system CPU per datagram.
	metricCPUPerDatagram string = "cpu-ns/datagram"
	// metricDatagramsPerSyscall is how many datagrams one receive returned on
	// average.
	metricDatagramsPerSyscall string = "datagrams/syscall"

	// minSamples is the plan's n: ten runs of each engine. Fewer is refused,
	// never judged on a wider interval.
	minSamples int = 10
	// confidence is benchstat's default level.
	confidence float64 = 0.95

	// chainCPUCeiling is P2's: the middleware chain costs at most 5 % more CPU
	// a request than the same auth and routing written by hand in one handler.
	chainCPUCeiling float64 = 1.05
	// gsoDatagramsFloor is P4a's: at least four times the datagrams a receive.
	gsoDatagramsFloor float64 = 4
	// gsoCPUCeiling is P4a's: at least 25 % less CPU a datagram.
	gsoCPUCeiling float64 = 0.75
	// loopsCPUCeiling is P6's one threshold: at least 30 % less CPU a request
	// than the goroutine engine.
	loopsCPUCeiling float64 = 0.70
	// p2AllocBudget is where P2's "allocs 0" is held: a process-wide count
	// across the bench's child would count the runtime's own allocations too
	// (measured: about 0.00035 a request on L1 and L2), and the SDK refuses a
	// gate that reads one (scripts/pre-commit/check-alloc-lane-coverage.sh).
	p2AllocBudget string = "0 allocations a request on the chain's established path: a kit budget of design/net.yaml, counted on its own stack by the race-off alloc lane (tools/alloc-lane-targets.txt), the exclusions below aside"
	// publishedCPUCeiling is P5's: no more CPU a request than the published
	// http1.
	publishedCPUCeiling float64 = 1.0
)

var (
	// allocExclusions are the paths a 0-alloc budget does not count, by name.
	// The bench never measures them either: setup and Start run before its
	// window, an error fails it, and no load fills a queue.
	allocExclusions = []string{"full queue", "setup/Start", "error paths"}

	// p2Criteria: the chain's CPU a request. Its 0 allocations are a budget,
	// not a bench metric (gateEntity.AllocBudget).
	p2Criteria = []criterionEntity{{Metric: metricCPUPerReq, Op: opAtMost, Threshold: chainCPUCeiling, Ratio: true}}
	// p4aCriteria: datagrams a receive, and CPU a datagram.
	p4aCriteria = []criterionEntity{
		{Metric: metricDatagramsPerSyscall, Op: opAtLeast, Threshold: gsoDatagramsFloor, Ratio: true},
		{Metric: metricCPUPerDatagram, Op: opAtMost, Threshold: gsoCPUCeiling, Ratio: true},
	}
	// p6Criteria: CPU a request against the goroutine engine.
	p6Criteria = []criterionEntity{{Metric: metricCPUPerReq, Op: opAtMost, Threshold: loopsCPUCeiling, Ratio: true}}
	// p5Criteria: CPU a request against the published http1.
	p5Criteria = []criterionEntity{{Metric: metricCPUPerReq, Op: opAtMost, Threshold: publishedCPUCeiling, Ratio: true}}

	// gates is the plan's table (P8 §Gates). P0, P1 and P3 have functional
	// gates only (the lanes), so they have no row here. No step a row gates
	// is built yet, so every row is inactive: reported, never failing.
	gates = []gateEntity{
		{Step: "P2", Load: loadL2, A: "handwritten", B: "chain", Criteria: p2Criteria,
			AllocBudget: p2AllocBudget, Pending: "the http1 middleware chain (P2) is engine B"},
		{Step: "P4a", Load: loadL3, A: "recvmmsg", B: "gro", Criteria: p4aCriteria,
			Pending: "GSO/GRO and sendmmsg in datagramSource (P4a) is engine B"},
		{Step: "P6", Load: loadL1, A: "goroutine", B: "loop", Criteria: p6Criteria,
			Pending: "LoopsAuto and the drain (P6) set engine B's loop count"},
		{Step: "P5", Load: loadL1, A: "published", B: "loop", Criteria: p5Criteria,
			Pending: "the published http1's L1 samples (engine A) are not stored yet; P5 widens the profile"},
	}
)
