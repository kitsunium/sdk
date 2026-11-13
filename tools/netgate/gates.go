// Package main — the gate table: the merge gates of the network plan (P8, R7:
// a gate is placed at the merge, never at the release), written as data. One
// row per step, each comparing engine B against engine A on one load, both
// served by one child in each round, side by side, in windows A, B, B, A, …:
// a round is one sample of the ratio B/A. A criterion is one metric and one
// threshold, judged on the bound of the ratios' confidence interval that can
// fail it (stats.go, eval.go). The calibration rows hold one engine against
// itself: the ratio two series of the same engine give on the runner, which
// must stay within the tightest threshold the load is judged at, or no row
// of that load can be trusted to pass or fail on its engines alone.
package main

const (
	// opAtMost passes when the interval's UPPER bound is at or below the
	// threshold: the worst the samples allow still meets it.
	opAtMost string = "<="
	// opAtLeast passes when the interval's LOWER bound is at or above it.
	opAtLeast string = ">="

	// loadL1 is a plaintext GET with a 1 KiB body over 1000 keep-alive
	// connections. The loads are fixed in the bench code of
	// internal/service/net/server (load_gate_test.go,
	// load_l3_linux_internal_test.go): the gate names them, the bench defines
	// them.
	loadL1 string = "L1"
	// loadL2 is L1 with a Bearer token and routing.
	loadL2 string = "L2"
	// loadL1h2 is L1's request over h2c by prior knowledge: 1000 kept-alive
	// connections, eight streams in flight on each, the client writing
	// pre-encoded frames (#963).
	loadL1h2 string = "L1h2"
	// loadL1big is L1's request answered with a 64 KiB body, its rounds an
	// eighth of L1's requests (load_big_linux_test.go): the size a copy is
	// worth avoiding, for the zero-copy send (#969) and the file tree on the
	// loop (#982).
	loadL1big string = "L1big"
	// loadSparse is the sparse keep-alive load over the veth pair: requests
	// at a fixed rate over connections kept open, each asked rarely, the
	// wake each costs what busy polling removes (#979,
	// load_sparse_veth_linux_test.go).
	loadSparse string = "Sparse"
	// loadL3 is UDP, 1200-byte datagrams from a GSO sender of 54 segments, 4
	// flows at a fixed rate, over a veth pair.
	loadL3 string = "L3"

	// metricCPUPerReq is the server process's user+system CPU per request
	// (testing.B.ReportMetric units, as the benches report them).
	metricCPUPerReq string = "cpu-ns/req"
	// metricCPUPerDatagram is the CPU a datagram costs: the receiver's and the
	// sender's rusage, and the irq and softirq time /proc/stat accounts to no
	// task — where GRO saves, a GSO send being segmented on receive without it
	// (the owner's decision of 2026-10-26, ADR 0192 §D6). Each part is
	// reported beside, ungated (rcv-, snd-, irq-cpu-ns/datagram). /proc/stat's
	// busy time, the first measure, fell below the receiver's own rusage on
	// one GitHub runner in two, and the bench now refuses such a window.
	metricCPUPerDatagram string = "cpu-ns/datagram"
	// metricDatagramsPerSyscall is how many datagrams one receive returned on
	// average.
	metricDatagramsPerSyscall string = "datagrams/syscall"
	// metricP99 is the 99th percentile of a request's round trip, as the
	// client times it.
	metricP99 string = "p99-ns"

	// minSamples is the plan's n: ten runs of each engine. Fewer is refused,
	// never judged on a wider interval.
	minSamples int = 10
	// confidence is benchstat's default level.
	confidence float64 = 0.95

	// chainCPUCeiling is P2's: the middleware chain costs at most 5 % more CPU
	// a request than the same auth and routing written by hand in one handler.
	chainCPUCeiling float64 = 1.05
	// gsoDatagramsFloor is P4a's reported floor: four times the datagrams a
	// receive, measured and written in the report, not gated.
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
	// calibrationHTTP is how far from 1 two series of one engine may lie on L1
	// and L2: P2's 5 %, the tightest ratio either load is judged at that a
	// faster B can meet (P5's 1.0 is a non-regression, met only below 1).
	calibrationHTTP float64 = 1.05
	// h2CPUCeiling is the reported h1-h2 row's: HTTP/2 costs no more CPU a
	// request than HTTP/1.1 on the same chain. Reported until measured.
	h2CPUCeiling float64 = 1.0
	// calibrationUDP is the same on L3: P4a's 25 %.
	calibrationUDP float64 = 1.25
	// sendZCCeiling is #969's: a zero-copy send at least 10 % cheaper a
	// request than the copying one, before uring-send-zc may leave class
	// A-code. Reported until then.
	sendZCCeiling float64 = 0.9
	// busyPollCeiling is #979's reported bound: busy polling no slower at
	// the 99th percentile and no dearer a request; its ADR sets the
	// threshold once the veth lane has measured it.
	busyPollCeiling float64 = 1.0
)

var (
	// allocExclusions are the paths a 0-alloc budget does not count, by name.
	// The bench never measures them either: setup and Start run before its
	// window, an error fails it, and no load fills a queue.
	allocExclusions = []string{"full queue", "setup/Start", "error paths"}

	// p2Criteria: the chain's CPU a request. Its 0 allocations are a budget,
	// not a bench metric (gateEntity.AllocBudget).
	p2Criteria = []criterionEntity{{Metric: metricCPUPerReq, Op: opAtMost, Threshold: chainCPUCeiling, Ratio: true}}
	// p4aCriteria: CPU a datagram, gated; datagrams a receive, reported and
	// not gated (the owner's decision of 2026-10-26, ADR 0192 §D6): on L3,
	// engine A already reads a GSO send's 54 datagrams a receive, so B's
	// count measures how many sends queue before its reader wakes — the
	// runner's scheduling, not the engine (3.54 on a GitHub runner, 4.73
	// under Docker, the same code).
	p4aCriteria = []criterionEntity{
		{Metric: metricDatagramsPerSyscall, Op: opAtLeast, Threshold: gsoDatagramsFloor, Ratio: true, Reported: true},
		{Metric: metricCPUPerDatagram, Op: opAtMost, Threshold: gsoCPUCeiling, Ratio: true},
	}
	// p6Criteria: CPU a request against the goroutine engine.
	p6Criteria = []criterionEntity{{Metric: metricCPUPerReq, Op: opAtMost, Threshold: loopsCPUCeiling, Ratio: true}}
	// p5Criteria: CPU a request against the published http1.
	p5Criteria = []criterionEntity{{Metric: metricCPUPerReq, Op: opAtMost, Threshold: publishedCPUCeiling, Ratio: true}}
	// calHTTPCriteria: one engine against itself, within calibrationHTTP of 1
	// either way.
	calHTTPCriteria = []criterionEntity{
		{Metric: metricCPUPerReq, Op: opAtMost, Threshold: calibrationHTTP, Ratio: true},
		{Metric: metricCPUPerReq, Op: opAtLeast, Threshold: 1 / calibrationHTTP, Ratio: true},
	}
	// uringRecvmsgCriteria: P4a's on the io_uring datagram lane (#852):
	// CPU a datagram gated at 0.75, datagrams a batch reported.
	uringRecvmsgCriteria = []criterionEntity{
		{Metric: metricDatagramsPerSyscall, Op: opAtLeast, Threshold: gsoDatagramsFloor, Ratio: true, Reported: true},
		{Metric: metricCPUPerDatagram, Op: opAtMost, Threshold: gsoCPUCeiling, Ratio: true},
	}
	// shardsCriteria: four sockets of one port against one, CPU a datagram
	// at L3's fixed rate within the calibration's 25 %, reported (#977).
	shardsCriteria = []criterionEntity{{Metric: metricCPUPerDatagram, Op: opAtMost, Threshold: calibrationUDP, Ratio: true, Reported: true}}
	// sendZCCriteria: the zero-copy send against the copying one, at 64 KiB.
	sendZCCriteria = []criterionEntity{{Metric: metricCPUPerReq, Op: opAtMost, Threshold: sendZCCeiling, Ratio: true, Reported: true}}
	// filesCriteria: the file tree on the loop against net/static on the
	// goroutine engine, at P6's threshold, reported (#982).
	filesCriteria = []criterionEntity{{Metric: metricCPUPerReq, Op: opAtMost, Threshold: loopsCPUCeiling, Ratio: true, Reported: true}}
	// busyPollCriteria: the 99th percentile and the CPU a request, each
	// against the loop that sleeps, reported (#979).
	busyPollCriteria = []criterionEntity{
		{Metric: metricP99, Op: opAtMost, Threshold: busyPollCeiling, Ratio: true, Reported: true},
		{Metric: metricCPUPerReq, Op: opAtMost, Threshold: busyPollCeiling, Ratio: true, Reported: true},
	}
	// h1h2Criteria: the loop's HTTP/2 against its HTTP/1.1, reported, no
	// threshold binding until measured on the runner.
	h1h2Criteria = []criterionEntity{{Metric: metricCPUPerReq, Op: opAtMost, Threshold: h2CPUCeiling, Ratio: true, Reported: true}}
	// calUDPCriteria: the same on L3's two metrics, within calibrationUDP.
	calUDPCriteria = []criterionEntity{
		{Metric: metricCPUPerDatagram, Op: opAtMost, Threshold: calibrationUDP, Ratio: true},
		{Metric: metricCPUPerDatagram, Op: opAtLeast, Threshold: 1 / calibrationUDP, Ratio: true},
		{Metric: metricDatagramsPerSyscall, Op: opAtMost, Threshold: calibrationUDP, Ratio: true},
		{Metric: metricDatagramsPerSyscall, Op: opAtLeast, Threshold: 1 / calibrationUDP, Ratio: true},
	}

	// gates is the plan's table (P8 §Gates). P0, P1 and P3 have functional
	// gates only (the lanes), so they have no row here. P2 is active: its
	// chain (internal/service/net/http1's links) is engine B; so is P4a: GRO in
	// datagramSource (ADR 0192). P6 is active: engine B is the loop engine on
	// LoopsAuto, one loop a P but one. Every other step's row waits on its step,
	// inactive: reported, never failing.
	// The calibration rows are active: a runner on which one engine does not
	// match itself fails the run rather than lets a step pass or fail on it.
	gates = []gateEntity{
		{
			Step: "P2", Load: loadL2, A: "handwritten", B: "chain", Criteria: p2Criteria,
			AllocBudget: p2AllocBudget, Active: true,
		},
		{
			Step: "P4a", Load: loadL3, A: "recvmmsg", B: "gro", Criteria: p4aCriteria, Active: true,
		},
		{Step: "P6", Load: loadL1, A: "goroutine", B: "loop", Criteria: p6Criteria, Active: true},
		{
			Step: "P5", Load: loadL1, A: "published", B: "loop", Criteria: p5Criteria,
			Pending: "the published http1's L1 samples (engine A) are not stored yet; P5 widens the profile",
		},
		{Step: "cal-L1", Load: loadL1, A: "loop", B: "loop", Criteria: calHTTPCriteria, Active: true},
		{Step: "cal-L2", Load: loadL2, A: "handwritten", B: "handwritten", Criteria: calHTTPCriteria, Active: true},
		{Step: "cal-L3", Load: loadL3, A: "recvmmsg", B: "recvmmsg", Criteria: calUDPCriteria, Active: true},
		{
			Step: "P9", Load: loadL1h2, A: "goroutine-h2", B: "loop-h2", Criteria: p6Criteria,
			Pending: "the h2 lane is measured on the runner first: cal-L1h2 within 1.05 over three runs, then P9 is turned on (#963)",
		},
		{
			Step: "cal-L1h2", Load: loadL1h2, A: "loop-h2", B: "loop-h2", Criteria: calHTTPCriteria,
			Pending: "three consecutive runner rounds within 0.952-1.05, as cal-L1's, before it gates (#963)",
		},
		{
			Step: "h1-h2", Load: loadL1h2, A: "loop", B: "loop-h2", Criteria: h1h2Criteria,
			Pending: "reported only: HTTP/2 against HTTP/1.1 on the loop, no threshold until measured (#963)",
		},
		// #852: held on three runs before it was turned on, upper bound 0.31.
		{Step: "uring-recvmsg", Load: loadL3, A: "recvmmsg", B: "uring-recvmsg", Criteria: uringRecvmsgCriteria, Active: true},
		{
			Step: "shards", Load: loadL3, A: "recvmmsg", B: "recvmmsg+shards", Criteria: shardsCriteria,
			Pending: "reported: Shards(0) becomes the datagram default only once four shards serve 1.8 times one socket's datagrams at saturation, which L3's fixed rate does not measure (#977)",
		},
		{
			Step: "send-zc", Load: loadL1big, A: "loop", B: "loop-sendzc", Criteria: sendZCCriteria,
			Pending: "reported: uring-send-zc stays class A-code until the ratio holds, then leaves it only through a net:default-change pull request citing it (ADR 0189, #969)",
		},
		{
			Step: "files", Load: loadL1big, A: "goroutine-static", B: "loop-files", Criteria: filesCriteria,
			Pending: "reported: the file tree on the loop against net/static, no gate until measured on the runner (#982)",
		},
		{
			Step: "busy-poll", Load: loadSparse, A: "loop", B: "loop-busypoll", Criteria: busyPollCriteria,
			Pending: "reported: busy-poll stays class A-code; its ADR sets the p99 and CPU thresholds from the veth lane's rows (ADR 0189, #979)",
		},
		{
			Step: "kqueue", Load: loadL1, A: "goroutine", B: "loop-kqueue", Criteria: p6Criteria,
			Pending: "macos-15 lane (bench-darwin), judged alone with -only, reported: auto takes kqueue on darwin since this row held P6 on run 43358351908 (ADR 0222, #976)",
		},
		{
			Step: "cal-L1-kqueue", Load: loadL1, A: "loop-kqueue", B: "loop-kqueue", Criteria: calHTTPCriteria,
			Pending: "macos-15 lane: the darwin runner held within 1.05 of itself before kqueue's row can be judged (#976)",
		},
	}
)
