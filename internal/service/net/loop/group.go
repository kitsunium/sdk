package loop

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// idleSlotHeapBudget is the most Go heap, in bytes, one idle connection
// costs on a native backend — 256 bytes, its slot and its timer link, as the
// package documentation states. The memory test measures the same bound.
const idleSlotHeapBudget uintptr = 256

// A slot and its timer link — the wheel's next, prev and at entries — must
// fit idleSlotHeapBudget: past it the difference is a negative uintptr
// constant, which does not compile.
const _ uintptr = idleSlotHeapBudget - (unsafe.Sizeof(connCtl{}) + 2*unsafe.Sizeof(int32(0)) + unsafe.Sizeof(int64(0)))

// Listener is the one hand-off from server to the loop: a listening socket
// server has already bound or adopted, as a duplicated descriptor the loop
// group owns from then on and closes when it stops.
type Listener struct {
	// FD is the duplicated descriptor, close-on-exec.
	FD int
}

// Config is what server gives a loop group to start: the group's name, its
// LoopValue, the two options the engine honours itself — MaxConns and
// IdleTimeout — its handler and its listeners.
type Config struct {
	// Group is the group's name, for the start line and State.
	Group string
	// Value is the group's LoopValue.
	Value corenet.LoopValue
	// MaxConns is LimitsValue.MaxConns: past it a connection is accepted and
	// closed at once. Zero sets no ceiling.
	MaxConns int
	// IdleTimeout closes a connection with no traffic for that long, its
	// output sent. Zero sets none.
	IdleTimeout time.Duration
	// Handler is the group's loop handler.
	Handler corenet.LoopHandler
	// Listeners are the sockets to accept on.
	Listeners []Listener
	// Log receives the one start line; nil is standard error.
	Log io.Writer
	// tableCap, when positive, caps every ring's table below the descriptor
	// limit: the conformance suite's tiny slot table.
	tableCap int
}

// Group is a running loop group: its loops, their threads and their rings.
type Group struct {
	// name is the group's name.
	name string
	// backend, stage and reason are what the probe decided.
	backend corenet.LoopBackend
	stage   string
	reason  string
	// sz is the resolved configuration.
	sz sizing
	// grp is what the loops share.
	grp *shared
	// loops are the running loops.
	loops []*loop
	// ctl drives the loops' threads: drain, stop, wait.
	ctl runner
	// once closes the group once.
	once sync.Once
	// procs is GOMAXPROCS read at Start.
	procs int
}

// runner is a backend's handle on its threads.
type runner interface {
	// drain asks every loop to stop accepting and close its connections as
	// they settle.
	drain()
	// stop asks every loop to close everything now.
	stop()
	// wait blocks until every loop's thread has returned.
	wait()
	// done is closed once every loop's thread has returned.
	done() <-chan struct{}
}

// Start resolves cfg against the effective RLIMIT_NOFILE — refusing with
// LoopNoDescriptors when it is zero or unreadable, before the one ring formula
// divides by it — starts the group's loops on the backend cfg asks for, and
// writes one line to cfg.Log naming the backend, the probe stage, any fallback
// reason, the loops, the rings and their sizes. The listeners are the group's
// from the call on, started or refused. An explicit backend the platform has
// no mechanic for is UnsupportedPlatform; one the kernel's probe refused is
// LoopBackendUnavailable.
func Start(cfg Config) (*Group, error) {
	rlimit := descriptorLimit()
	sz, err := resolve(cfg.Value, cfg.MaxConns, rlimit)
	//: no descriptor to work with.
	if err != nil {
		closeListeners(cfg.Listeners)
		//: refused before any thread starts.
		return nil, err
	}
	//: a handler is what the group serves; none is a wiring mistake.
	if cfg.Handler == nil {
		closeListeners(cfg.Listeners)
		//: refused.
		return nil, errs.Wrap(corenet.HandlerMissing, errs.WrapParams{}, errs.String("group", cfg.Group))
	}
	//: a test's tiny table.
	if cfg.tableCap > 0 {
		sz.tableSize = cfg.tableCap
	}
	g := &Group{name: cfg.Group, sz: sz, grp: &shared{}, procs: runtime.GOMAXPROCS(0)}
	//: the platform's native backends, or a refusal.
	if err := startNative(g, &cfg); err != nil {
		closeListeners(cfg.Listeners)
		//: refused, with the probe's reason.
		return nil, err
	}
	w := cfg.Log
	//: standard error unless told otherwise.
	if w == nil {
		w = os.Stderr
	}
	_, werr := fmt.Fprintf(w, "net/loop: group=%q backend=%s probe=%s reason=%q loops=%d rings=%d rings_source=%s table=%d cq_batch=%d spill_cap=%d output_cap=%d rlimit_nofile=%d gomaxprocs=%d\n",
		g.name, g.backend, g.stage, g.reason, sz.loops, sz.rings, sz.ringsSource, sz.tableSize, sz.cqBatch, sz.spillCap, sz.outputCap, sz.rlimit, g.procs)
	//: a log writer that fails changes nothing: the group serves either way.
	swallowErr(werr)
	//: serving.
	return g, nil
}

// Shutdown stops accepting and waits for every connection to settle, its
// output sent, until ctx is done; then it closes the rest. A drain cut short
// returns DrainTimeout.
func (g *Group) Shutdown(ctx context.Context) error {
	g.ctl.drain()
	//: settled, or out of budget.
	select {
	//: every loop returned on its own.
	case <-g.ctl.done():
		//: drained.
		return nil
	//: the budget ran out first.
	case <-ctx.Done():
		g.ctl.stop()
		g.ctl.wait()
		//: what was still open was closed.
		return errs.Wrap(corenet.DrainTimeout, errs.WrapParams{}, errs.String("group", g.name))
	}
}

// Close closes every connection and stops every loop now.
func (g *Group) Close() error {
	g.once.Do(func() {
		g.ctl.stop()
		g.ctl.wait()
	})
	//: stopped.
	return nil
}

// State reports the group's backend and every ring's counters.
func (g *Group) State() corenet.LoopStateValue {
	st := corenet.LoopStateValue{
		Group: g.name, Backend: g.backend, ProbeStage: g.stage,
		Degraded: g.reason != "", DegradedReason: g.reason,
		Loops: g.sz.loops, Rings: g.sz.rings, RingsSource: g.sz.ringsSource,
		RLimitNoFile: g.sz.rlimit, GOMAXPROCS: g.procs,
		BatchP99: corenet.DurationValue(g.grp.batchP99()),
	}
	//: loop by loop, ring by ring.
	for _, lp := range g.loops {
		//: every ring of the loop.
		for _, r := range lp.rings {
			p := r.pub
			st.RingStates = append(st.RingStates, corenet.LoopRingStateValue{
				Loop: lp.index, Ring: r.index, Conns: p.conns.Load(),
				Accepted: p.accepted.Load(), Closed: p.closed.Load(),
				Batches: p.batches.Load(), Completions: p.completions.Load(),
				BufferStarved: p.starved.Load(), EnterRetries: p.retries.Load(),
				AcceptPauses: p.pauses.Load(), Evicted: p.evicted.Load(),
				WatchdogFires: p.watchdog.Load(), Draining: p.draining.Load(),
			})
		}
	}
	//: a snapshot.
	return st
}

// Rejected is how many connections MaxConns turned away.
func (g *Group) Rejected() uint64 {
	//: counted at accept.
	return g.grp.rejected.Load()
}

// Live is how many connections the group serves now.
func (g *Group) Live() int64 {
	//: counted at open and close.
	return g.grp.live.Load()
}

// newLoop builds loop i of g, its rings empty, its slab mapped by mapper.
func newLoop(g *Group, cfg Config, i int, mapper func(int) ([]byte, error)) *loop {
	lp := &loop{
		index: i, sz: g.sz, h: cfg.Handler, grp: g.grp,
		epoch:    time.Now(),
		lowWater: g.sz.outputCap / lowWaterDiv,
	}
	//: the idle bound and the eviction age, in ticks, rounded up.
	if cfg.IdleTimeout > 0 {
		lp.idle = int64((cfg.IdleTimeout + tickEvery - 1) / tickEvery)
	}
	//: eviction is opt-in.
	if d := time.Duration(cfg.Value.EvictIdle); d > 0 {
		lp.evictIdle = int64((d + tickEvery - 1) / tickEvery)
	}
	lp.slab = newSlab(max(g.sz.outputCap, g.sz.spillCap), defaultSlabBudget, mapper)
	lp.tick = 1
	//: rings are added by the backend.
	return lp
}

// swallowErr intentionally discards a non-actionable error, recording the
// discard so the error audit treats it as deliberate rather than dropped.
//
// The loop has no caller to return to on these paths: a descriptor it closes
// is already out of its tables, a mapping it unmaps is already out of its
// slab, an epoll_ctl on a listener or a connection the loop is about to drop
// or retry at the next readiness, and the start line's writer is the
// caller's own object. None of them changes what the loop does next.
func swallowErr(err error) {
	//: read the parameter so the unused-error audit treats this as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
	//: the failure concerns a resource the loop is already finished with.
}
