package loop

import (
	"os"
	"runtime"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// ioringMaxFixedFiles is IORING_MAX_FIXED_FILES, the most entries one
	// io_uring file table holds (io_uring/rsrc.h, 1 << 20 since 5.11).
	ioringMaxFixedFiles uint64 = 1 << 20
	// defaultCQBatch is how many completions or events one iteration reaps
	// when LoopValue.CQBatch is zero.
	defaultCQBatch int = 512
	// defaultSpillCap is LoopValue.SpillCap's default: one request head and
	// a modest body, unconsumed, per connection.
	defaultSpillCap int = 64 << 10
	// defaultOutputCap is LoopValue.OutputCap's default.
	defaultOutputCap int = 1 << 20
	// ringsExplicit is a Rings the LoopValue set; the three ringsX values
	// are where Rings came from, as State reports it.
	ringsExplicit string = "explicit"
	// ringsDerived is a Rings the formula derived: ⌈target / table⌉, more
	// than one.
	ringsDerived string = "derived"
	// ringsSingle is one ring: no bound to size against, or one table holds
	// the target.
	ringsSingle string = "single"
	// loopsExplicit is a positive LoopValue.Loops; the three loopsX values
	// are where Loops came from, as State reports it.
	loopsExplicit string = "explicit"
	// loopsAuto is LoopsAuto: GOMAXPROCS less one read at Start.
	loopsAuto string = "auto"
	// loopsSingle is a zero LoopValue.Loops: one loop (R8).
	loopsSingle string = "single"
	// defaultBatchWait is LoopValue.BatchWait's default: the most the
	// grouped io_uring wait adds to a request's latency (ADR 0199).
	defaultBatchWait time.Duration = 250 * time.Microsecond
	// defaultBatchWaitMin is LoopValue.BatchWaitMin's default.
	defaultBatchWaitMin int = 32
	// defaultSendZCMin is LoopValue.SendZCMin's default: below about 10 KiB a
	// zero-copy send's page pinning and notification cost more than the copy
	// it saves (the kernel's MSG_ZEROCOPY guidance); the gate of #969 is to
	// measure it.
	defaultSendZCMin int = 16 << 10
	// batchWaitFrom is the fewest completions a batch must have reaped for
	// the next wait to be grouped: below it the loop is not loaded, and
	// waiting would only add latency.
	batchWaitFrom uint64 = 4
	// spacingWeight is the weight of one batch in the moving average of the
	// time between two arrivals (ADR 0202): an eighth, so a burst of a few
	// batches does not swing it, and a few dozen settle it.
	spacingWeight time.Duration = 8
	// yieldEvery is the most serving time between two yields of a native
	// loop to the Go scheduler, when GOMAXPROCS leaves it a spare P (ADR
	// 0199). Measured: 2 ms and 5 ms cost the same.
	yieldEvery time.Duration = 5 * time.Millisecond
)

// The sources LoopStateValue.SlabBudgetSource names (#809).
const (
	// slabExplicit is LoopValue.SlabBudget, as given.
	slabExplicit string = "explicit"
	// slabDerived is a quarter of the cgroup allowance, shared by the loops.
	slabDerived string = "derived"
	// slabDefault is defaultSlabBudget: no allowance was found.
	slabDefault string = "default"
	// slabAllowanceShare is the share of the allowance every loop's slab
	// together may map: a quarter, the rest the Go heap's and the kernel's.
	slabAllowanceShare int64 = 4
)

// sizing is a loop group's resolved configuration: every zero of the
// LoopValue replaced by its default, the ring count derived once.
type sizing struct {
	// loops is the number of loops; loopsSource where it came from.
	loops       int
	loopsSource string
	// slabBudget is each loop's slab ceiling, slabSource where it came from
	// (#809); zero only in a test's hand-built sizing, read as the default.
	slabBudget int
	slabSource string
	// rings is the number of rings per loop.
	rings int
	// ringsSource is where rings came from.
	ringsSource string
	// tableSize is the most connections one ring holds.
	tableSize int
	// cqBatch bounds one iteration's reaping.
	cqBatch int
	// spillCap and outputCap are the per-connection byte ceilings.
	spillCap  int
	outputCap int
	// softConns flags pressure past it; zero sets no soft bound.
	softConns int
	// maxConns refuses past it; zero sets no ceiling.
	maxConns int
	// rlimit is the effective RLIMIT_NOFILE soft limit.
	rlimit uint64
	// batchWait bounds io_uring's grouped wait, zero when it is off;
	// batchWaitMin is the completions it waits for.
	batchWait    time.Duration
	batchWaitMin int
	// batchBudget bounds one io_uring batch's reaping, zero for none (ADR
	// 0206).
	batchBudget time.Duration
	// sendZCMin is the smallest flight sent zero-copy once uring-send-zc is
	// on (#969).
	sendZCMin int
	// busyPoll is busy-poll's window and budget, used once the row is on
	// (#979).
	busyPoll busyPollPlan
	// zcrx is the queue uring-zcrx's probe registers, its area resolved
	// (#980).
	zcrx corenet.ZCRXValue
	// yield is the most serving time between two yields to the scheduler,
	// zero for none: Start sets it (yieldFor), once GOMAXPROCS is read.
	yield time.Duration
}

// resolve applies the defaults and the one ring formula of ADR 0186 §D5 to v,
// against the effective descriptor limit. A limit of zero leaves no
// descriptor for anything, and is refused before the division it would
// otherwise reach.
func resolve(v corenet.LoopValue, maxConns int, rlimit uint64) (sizing, error) {
	//: no descriptor for a ring's table nor for a connection: refused first,
	//: whatever Rings says.
	if rlimit == 0 {
		//: never a division by zero, never a ring that can accept nothing.
		return sizing{}, errs.Wrap(corenet.LoopNoDescriptors, errs.WrapParams{})
	}
	table := min(rlimit, ioringMaxFixedFiles)
	sz := sizing{
		cqBatch:   orDefault(v.CQBatch, defaultCQBatch),
		spillCap:  orDefault(v.SpillCap, defaultSpillCap),
		outputCap: orDefault(v.OutputCap, defaultOutputCap),
		softConns: max(v.SoftConns, 0),
		maxConns:  max(maxConns, 0),
		rlimit:    rlimit,
		tableSize: int(table),
	}
	sz.batchWait, sz.batchWaitMin = batchWaitOf(v, sz.cqBatch)
	sz.batchBudget = max(time.Duration(v.BatchBudget), 0)
	sz.sendZCMin = orDefault(v.SendZCMin, defaultSendZCMin)
	sz.busyPoll = busyPollOf(v)
	sz.zcrx = zcrxOf(v, os.Getpagesize())
	sz.loops, sz.loopsSource = ResolveLoops(v.Loops)
	sz.rings, sz.ringsSource = ringCount(v.Rings, max(sz.maxConns, sz.softConns), table)
	//: resolved.
	return sz, nil
}

// ringCount is the one formula: the explicit value, otherwise
// ⌈target / table⌉, and one when no bound was set.
func ringCount(explicit, target int, table uint64) (int, string) {
	//: the consumer's own count wins.
	if explicit > 0 {
		//: as asked.
		return explicit, ringsExplicit
	}
	//: no bound to size against.
	if target <= 0 {
		//: one ring holds whatever the limit allows.
		return 1, ringsSingle
	}
	n := (uint64(target) + table - 1) / table
	//: one ring is the single case, not a derivation worth naming.
	if n <= 1 {
		//: one ring suffices.
		return 1, ringsSingle
	}
	//: as many tables as the target needs.
	return int(n), ringsDerived
}

// batchWaitOf resolves the grouped wait: BatchWait's default, off when
// negative, never past the tick; BatchWaitMin's default, never past cqBatch,
// since one batch reaps no more.
func batchWaitOf(v corenet.LoopValue, cqBatch int) (time.Duration, int) {
	d := time.Duration(v.BatchWait)
	//: switched off.
	if d < 0 {
		//: the plain wait.
		return 0, 0
	}
	//: the default.
	if d == 0 {
		d = defaultBatchWait
	}
	//: no longer than the tick, no more than one batch reaps.
	return min(d, tickEvery), min(orDefault(v.BatchWaitMin, defaultBatchWaitMin), cqBatch)
}

// yieldFor is the yield interval of a group of loops in a process of procs
// Ps: yieldEvery while a P is left over, none otherwise — with every P
// running a loop, a yield only hands the loop's own P around (ADR 0199).
func yieldFor(procs, loops int) time.Duration {
	//: a spare P for what the yield lets run.
	if procs > loops {
		//: bounded.
		return yieldEvery
	}
	//: no yield.
	return 0
}

// ResolveLoops resolves LoopValue.Loops n and says where the count came from,
// as LoopStateValue.LoopsSource spells it: LoopsAuto is GOMAXPROCS less one
// read now, at least one, "auto" — the P left over runs the rest of the
// process (ADR 0199); a positive count is its own, "explicit"; anything else is one,
// "single" (R8). server calls it once per group, at Start, and keeps the
// snapshot for every backend.
func ResolveLoops(n int) (int, string) {
	//: the consumer's count. The order of the tests keeps the function
	//: under the inlining budget.
	if n > 0 {
		//: as asked.
		return n, loopsExplicit
	}
	//: zero, or anything but the sentinel, is one (R8).
	if n != corenet.LoopsAuto {
		//: one.
		return 1, loopsSingle
	}
	//: one loop a P but one, read once.
	return max(runtime.GOMAXPROCS(0)-1, 1), loopsAuto
}

// orDefault returns v, or def when v is zero or less.
func orDefault(v, def int) int {
	//: zero selects the default, never "unbounded".
	if v <= 0 {
		//: the domain default.
		return def
	}
	//: as configured.
	return v
}

// slabBudgetFor is each loop's slab budget and where it came from (#809):
// the caller's own, else a quarter of the cgroup allowance memlimit read
// shared by loops loops, at most defaultSlabBudget and at least floor — one
// connection's largest chunk, so a loop can serve one —, else
// defaultSlabBudget.
func slabBudgetFor(explicit int, mem coreproc.MemoryLimitValue, loops, floor int) (int, string) {
	//: the caller's.
	if explicit > 0 {
		//: as given.
		return explicit, slabExplicit
	}
	//: no allowance read: the default.
	if mem.Allowance <= 0 {
		//: 128 MiB.
		return defaultSlabBudget, slabDefault
	}
	share := mem.Allowance / (slabAllowanceShare * int64(max(loops, 1)))
	//: within the default and the floor.
	return int(max(min(share, int64(defaultSlabBudget)), int64(floor))), slabDerived
}
