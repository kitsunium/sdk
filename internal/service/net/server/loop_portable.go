package server

import (
	"context"
	"errors"
	"io"
	stdnet "net"
	"os"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/loop"
)

const (
	// portableReadSize is one connection's read buffer on the portable
	// backend.
	portableReadSize int = 16 << 10
	// portableTick is the portable backend's timer resolution.
	portableTick time.Duration = 10 * time.Millisecond
	// portableQuiet is how long the drain leaves open a connection that has
	// sent nothing yet, as the native backends' drainQuiet: its request may be
	// on the way, and a close before it is read resets the peer.
	portableQuiet time.Duration = time.Second
	// portableEvents bounds the events one batch takes.
	portableEvents int = 512
	// portablePostCap is the portable backend's post queue.
	portablePostCap int = 4096
	// portableSpillCap is LoopValue.SpillCap's default, as the native
	// backends have it.
	portableSpillCap int = 64 << 10
	// portableOutputCap is LoopValue.OutputCap's default, as the native
	// backends have it.
	portableOutputCap int = 1 << 20
	// recycledMark is the byte a released buffer is filled with under
	// markRecycled.
	recycledMark byte = 0xAA
)

// The kinds of a portable event.
const (
	pevOpen uint8 = iota
	pevData
	pevEnd
	pevSent
)

// markRecycled, set by a test, fills each read buffer the batch released
// with recycledMark before it is read into again.
var markRecycled atomic.Bool

// portableEvent is what a connection's goroutines tell the handler goroutine.
type portableEvent struct {
	kind uint8
	slot int32
	gen  uint32
	conn stdnet.Conn
	data []byte
	err  error
}

// portableConn is one connection on the portable backend. Its reader and
// writer goroutines touch only conn, mu's fields and the channels; everything
// else is the handler goroutine's.
type portableConn struct {
	conn stdnet.Conn
	gen  uint32
	// ack lets the reader reuse its buffer once the batch that holds it ended.
	ack chan struct{}
	// kick wakes the writer.
	kick chan struct{}
	// mu guards out, flight and closing, shared with the writer.
	mu      sync.Mutex
	out     []byte
	flight  int
	closing bool
	// The handler goroutine's own state.
	open     bool
	spill    []byte
	spillOff int
	lastIO   time.Time
	deadline time.Time
	// heardAt is when the peer's first byte came, zero until then: the drain
	// leaves a connection that has sent nothing open for portableQuiet.
	heardAt   time.Time
	pressure  bool
	wantWrite bool
	peerDone  bool
	closeErr  error
	reasonSet bool
	// compact lists the connection on its loop's compaction list.
	compact bool
}

// portableGroup is the portable backend (ADR 0186 §D3, §D7): the default
// engine's accept path and a goroutine pair per connection on the netpoller,
// spread over LoopValue.Loops lanes, every callback of one lane serialised
// onto that lane's handler goroutine, so the LoopHandler contract holds
// unchanged: one goroutine calls a lane, buf valid until its OnBatchEnd.
// The acceptors hand connections to the lanes in turn.
type portableGroup struct {
	group    string
	soft     int
	maxConns int
	degraded string
	// loopsSource is where the lane count came from, for State; procs is
	// GOMAXPROCS read at Start.
	loopsSource string
	procs       int
	// caps is the group's capability report: every row unprobed, or
	// disabled; noDelayOff is tcp-nodelay switched off, so each accepted
	// connection has the netpoller's TCP_NODELAY cleared (ADR 0189).
	caps       []corenet.CapabilityValue
	noDelayOff bool
	lanes      []*portableLoop
	lns        []stdnet.Listener
	// owned are the listeners the group duplicated from server's and closes
	// itself, once its drain took their queue (takeQueues); one it could not
	// duplicate is server's, closed with the others after the grace.
	owned []stdnet.Listener
	// accepting counts the acceptors still running: a lane stops only once
	// none can hand it a connection.
	accepting atomic.Int32
	// handshake bounds a TLS connection's negotiation, which runs before the
	// lane sees it; zero for a plaintext group (ADR 0198).
	handshake time.Duration
	// turn is the lane the next accepted connection goes to, modulo the count.
	turn     atomic.Uint64
	drainReq atomic.Bool
	stopReq  atomic.Bool
	rejected atomic.Uint64
	once     sync.Once
}

// live is how many connections the group's lanes serve together.
func (g *portableGroup) live() int64 {
	var n int64
	//: one atomic load a lane.
	for _, pl := range g.lanes {
		n += pl.live.Load()
	}
	//: the group's count.
	return n
}

// portableLoop is one lane of the portable backend: its handler goroutine,
// its connections, its post queue and its LoopIO.
type portableLoop struct {
	g    *portableGroup
	lane int
	h    corenet.LoopHandler
	// busy is the chain's offloader's Pending, nil when nothing offloads.
	busy     func(lane int, ref corenet.LoopConnRef) bool
	spillCap int
	outCap   int
	idle     time.Duration

	events chan portableEvent
	// post is the queue of posted operations; wake rouses the handler.
	postMu   sync.Mutex
	postOps  []corenet.PostFunc
	postArgs []uintptr
	postHead int
	postN    int
	postWait atomic.Bool
	postShut bool
	wake     chan struct{}

	conns []*portableConn
	free  []int32
	live  atomic.Int64
	// negotiating counts the TLS handshakes still running for the lane: the
	// drainer closeAll starts outlives them, so none hands over unseen.
	negotiating atomic.Int64

	draining bool
	done     chan struct{}
	accepted atomic.Uint64
	closed   atomic.Uint64
	batches  atomic.Uint64
	readers  sync.WaitGroup
	// batchAcks are the readers waiting on the current batch.
	batchAcks []chan struct{}
	dirty     []int32
	// compact lists the slots whose spill's head was consumed in the batch.
	compact []int32
	// timersAt is when the next scan of the timers is due: a tick after the
	// last, or the earliest deadline set since, whichever comes first.
	timersAt time.Time
}

// startPortable starts the portable backend on lns, with one lane per loop
// of the group's LoopValue (zero meaning one). degraded names why a Linux
// auto group fell back to it, empty otherwise.
//
// Goroutine lifecycle: it launches one acceptor per listener, which returns
// once server closes that listener or the backend stops, and one handler
// goroutine per lane, which returns once the backend stopped or its lane
// drained to its last connection, closing the lane's done as it does.
func startPortable(group *StreamGroup, lns []stdnet.Listener, degraded string, plan loopPlan) portableEngine {
	v := group.loop
	src := group.loopsSource
	//: started without bindLoopGroup's resolution: resolved here.
	if src == "" {
		v.Loops, src = loop.ResolveLoops(v.Loops)
	}
	g := &portableGroup{
		group: group.name, soft: max(v.SoftConns, 0), maxConns: max(group.limits.MaxConns, 0),
		degraded: degraded, loopsSource: src, procs: runtime.GOMAXPROCS(0),
		caps:       append(loop.Unprobed("portable", plan.disabled, plan.disabledBy), portableNoDelay(plan)),
		noDelayOff: slices.Contains(plan.disabled, corenet.CapabilityTCPNoDelay),
	}
	//: a TLS codec group negotiates each connection first.
	if group.tlsCodec() {
		g.handshake = group.handshakeBound()
	}
	//: the lanes, built before any acceptor can hand one a connection.
	for i := range loopLanes(v) {
		g.lanes = append(g.lanes, &portableLoop{
			g: g, lane: i, h: group.loopChain, busy: group.loopBusy,
			spillCap: orDefaultInt(v.SpillCap, portableSpillCap),
			outCap:   orDefaultInt(v.OutputCap, portableOutputCap),
			idle:     loopIdle(group),
			events:   make(chan portableEvent, portableEvents),
			postOps:  make([]corenet.PostFunc, portablePostCap), postArgs: make([]uintptr, portablePostCap),
			wake: make(chan struct{}, 1), done: make(chan struct{}),
		})
	}
	g.lns, g.owned = ownListeners(lns)
	//: one acceptor per listener; it ends when its listener closes.
	for _, ln := range g.lns {
		g.accepting.Add(1)
		go g.acceptLoop(ln)
	}
	//: one handler goroutine per lane.
	for _, pl := range g.lanes {
		go pl.run(pl.done)
	}
	//: serving.
	return portableEngine{g: g}
}

// portableNoDelay is the portable backend's tcp-nodelay row: the netpoller
// sets TCP_NODELAY on every connection it accepts, cleared once the operator
// switched it off.
func portableNoDelay(plan loopPlan) corenet.CapabilityValue {
	row := corenet.CapabilityValue{Name: corenet.CapabilityTCPNoDelay, Class: classKernel, Status: statusOn, Reason: "the netpoller's default"}
	//: switched off: cleared per connection.
	if slices.Contains(plan.disabled, corenet.CapabilityTCPNoDelay) {
		row.Status, row.Reason = statusDisabled, plan.disabledBy
	}
	//: the row.
	return row
}

// orDefaultInt returns v, or def when v is zero or less.
func orDefaultInt(v, def int) int {
	//: zero selects the default.
	if v <= 0 {
		//: the default.
		return def
	}
	//: as configured.
	return v
}

// acceptLoop hands each accepted connection to a lane, in turn.
func (g *portableGroup) acceptLoop(ln stdnet.Listener) {
	defer g.accepting.Add(-1)
	failures := 0
	//: until its listener closes.
	for {
		c, err := ln.Accept()
		//: closed, or a failure to back off from.
		if err != nil {
			//: server closed it.
			if errors.Is(err, stdnet.ErrClosed) || g.stopReq.Load() {
				//: done.
				return
			}
			failures++
			time.Sleep(acceptDelay(failures))
			continue
		}
		failures = 0
		g.admit(c)
	}
}

// admit hands c to a lane, in turn, or closes it past MaxConns, a refusal
// the peer sees — while draining too, as the native backends' admit does: the
// ceiling holds whatever the phase. A draining group admits otherwise: what
// waited in the queue with its request is served, never closed unread. A
// lane that stopped closes it.
func (g *portableGroup) admit(c stdnet.Conn) {
	//: MaxConns: accepted and closed.
	if g.maxConns > 0 && g.live() >= int64(g.maxConns) {
		g.rejected.Add(1)
		swallowErr(c.Close())
		//: refused.
		return
	}
	g.dispatch(c)
}

// takeQueues takes every owned listener's queue to its end, then closes the
// listener (ADR 0196 §D3): what the kernel completed before the close is
// served, not reset by it. Out of descriptors, it tries again each tick
// while a lane still has connections to close — closes free descriptors —
// and ctx lives; past either, what waits meets the close.
func (g *portableGroup) takeQueues(ctx context.Context) {
	takes := make([]portableTake, len(g.owned))
	tick := time.NewTicker(portableTick)
	defer tick.Stop()
	//: until every listener is closed.
	for {
		blocked := g.takeRound(takes)
		//: every one closed.
		if !blocked {
			//: taken.
			return
		}
		//: nothing will free a descriptor, or no budget is left.
		if g.live() == 0 || ctx.Err() != nil {
			g.giveUp(takes)
			//: what waits meets the close.
			return
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
}

// takeRound takes once from every owned listener not closed yet, closes
// each one whose queue is empty, and reports whether one waits for
// descriptors.
func (g *portableGroup) takeRound(takes []portableTake) bool {
	blocked := false
	//: each listener the group owns.
	for i, ln := range g.owned {
		//: closed already.
		if takes[i].done {
			continue
		}
		//: out of descriptors: next round.
		if takeQueue(ln, g.admit, &takes[i]) {
			blocked = true
			continue
		}
		takes[i].done = true
		swallowErr(ln.Close())
	}
	//: whether one waits.
	return blocked
}

// giveUp closes every owned listener not closed yet, and every descriptor
// accepted but never wrapped.
func (g *portableGroup) giveUp(takes []portableTake) {
	//: each one left.
	for i, ln := range g.owned {
		closeRaw(takes[i].held)
		takes[i].held = nil
		//: closed already.
		if !takes[i].done {
			swallowErr(ln.Close())
		}
	}
}

// closeOwned closes every listener the group owns.
func (g *portableGroup) closeOwned() {
	//: each one; a second close is harmless.
	for _, ln := range g.owned {
		swallowErr(ln.Close())
	}
}

// dispatch hands c to the next lane, in turn; a TLS connection is negotiated
// first, off the acceptor, so the lane sees it only once it is cleartext.
//
// Goroutine lifecycle: for a TLS group, one negotiate goroutine per
// connection, which returns once the handshake ends and the connection is
// handed to its lane or closed. A lane that stopped closes it.
func (g *portableGroup) dispatch(c stdnet.Conn) {
	pl := g.lanes[(g.turn.Add(1)-1)%uint64(len(g.lanes))]
	pl.live.Add(1)
	//: TLS: negotiated off the acceptor, before the lane sees it.
	if g.handshake > 0 {
		pl.negotiating.Add(1)
		go pl.negotiate(c, g.handshake)
		//: handed over.
		return
	}
	//: to the lane, unless it stopped: its queue is drained by nobody then.
	select {
	case pl.events <- portableEvent{kind: pevOpen, conn: c}:
	case <-pl.done:
		pl.live.Add(-1)
		swallowErr(c.Close())
	}
}

// negotiate completes c's TLS handshake within d, then hands c to the lane;
// a failed or stalled one closes it, and the lane never sees it.
//
// Goroutine lifecycle: one per accepted TLS connection, started by
// dispatch; it returns once the handshake ends and the connection is handed
// over, or the lane has stopped, which closes the connection instead.
func (pl *portableLoop) negotiate(c stdnet.Conn, d time.Duration) {
	defer pl.negotiating.Add(-1)
	//: given up: the lane never counted it.
	if !handshake(c, d) {
		pl.live.Add(-1)
		//: closed.
		return
	}
	select {
	case pl.events <- portableEvent{kind: pevOpen, conn: c}:
	case <-pl.done:
		pl.live.Add(-1)
		swallowErr(c.Close())
	}
}

// run is the handler goroutine: one batch per iteration. It is the one
// goroutine that closes done, as it returns.
func (pl *portableLoop) run(done chan<- struct{}) {
	defer close(done)
	tick := time.NewTicker(portableTick)
	defer tick.Stop()
	//: until stopped, or drained to the last connection.
	for {
		//: the first event of the batch, or a tick, or a wake.
		select {
		case ev := <-pl.events:
			pl.handle(&ev)
		case <-pl.wake:
		case <-tick.C:
		}
		pl.batch()
		//: finished.
		if pl.finished() {
			pl.closeAll()
			//: out.
			return
		}
	}
}

// finished reports whether the lane stops: the backend stopped, or the lane
// drained to its last connection with no acceptor left to hand it another.
//
// accepting is read before live: an acceptor counts its connection in live
// before it stops counting itself, so no acceptor and no connection read in
// that order means none can come.
func (pl *portableLoop) finished() bool {
	//: stopped.
	if pl.g.stopReq.Load() {
		//: out.
		return true
	}
	//: no acceptor first, then no connection.
	return pl.draining && pl.g.accepting.Load() == 0 && pl.live.Load() == 0
}

// batch serves the events queued behind the first one, the posts, the timers
// when they are due and the drain, then ends the batch. A batch costs what it
// touches: the timers' scan of every connection runs once a tick, not once a
// batch.
func (pl *portableLoop) batch() {
	pl.collect()
	pl.drainPosts()
	//: a tick since the last scan, or a deadline due.
	if now := time.Now(); !now.Before(pl.timersAt) {
		pl.timers(now)
	}
	pl.control()
	pl.h.OnBatchEnd(pl)
	pl.endBatch()
}

// collect takes the events already queued, up to a batch.
func (pl *portableLoop) collect() {
	//: bounded, so one batch cannot starve the timers.
	for range portableEvents {
		select {
		case ev := <-pl.events:
			pl.handle(&ev)
		default:
			//: nothing more queued.
			return
		}
	}
}

// handle serves one event.
func (pl *portableLoop) handle(ev *portableEvent) {
	//: by kind.
	switch ev.kind {
	//: a new connection.
	case pevOpen:
		pl.open(ev.conn)
	//: bytes, from the reader of slot.
	case pevData:
		pl.handleData(ev)
	//: the reader or the writer ended.
	case pevEnd:
		pl.handleEnd(ev)
	//: the writer sent a flight.
	case pevSent:
		pl.handleSent(ev)
	}
}

// handleData delivers a reader's bytes, and holds its buffer until the batch
// ends.
func (pl *portableLoop) handleData(ev *portableEvent) {
	pc := pl.lookupSlot(ev.slot, ev.gen)
	pl.batchAcks = append(pl.batchAcks, pl.conns[ev.slot].ack)
	//: a connection closed since.
	if pc == nil || !pc.open {
		//: dropped.
		return
	}
	pl.deliver(ev.slot, pc, ev.data)
}

// handleEnd serves the end of a connection's reader or writer.
func (pl *portableLoop) handleEnd(ev *portableEvent) {
	pc := pl.lookupSlot(ev.slot, ev.gen)
	//: already finished.
	if pc == nil || !pc.open {
		//: done.
		return
	}
	//: a clean end of input still lets the output leave.
	if errors.Is(ev.err, io.EOF) && !pc.reasonSet {
		pc.peerDone = true
		pl.markDirty(ev.slot)
		//: settled at the end of the batch.
		return
	}
	pl.finish(ev.slot, pc, ev.err)
}

// handleSent records a flight the writer sent, and fires OnWritable when a
// producer waits and the output fell under a quarter of its cap.
func (pl *portableLoop) handleSent(ev *portableEvent) {
	pc := pl.lookupSlot(ev.slot, ev.gen)
	//: closed since.
	if pc == nil || !pc.open {
		//: done.
		return
	}
	pc.lastIO = time.Now()
	pl.markDirty(ev.slot)
	//: someone waits for room.
	if pc.wantWrite && pl.pending(pc) < pl.outCap/4 {
		pc.wantWrite = false
		pl.h.OnWritable(pl, corenet.LoopConnRef{Slot: uint32(ev.slot), Gen: pc.gen})
	}
}

// open takes a slot for c and starts its reader and writer.
//
// Goroutine lifecycle: it launches the connection's reader, which returns at
// the end of input or a read failure, and its writer, which returns once kick
// is closed by finish, the connection closing with its output sent, or a
// write failure.
func (pl *portableLoop) open(c stdnet.Conn) {
	//: tcp-nodelay switched off: the netpoller's default cleared.
	if tc, ok := c.(*stdnet.TCPConn); ok && pl.g.noDelayOff {
		swallowErr(tc.SetNoDelay(false))
	}
	var slot int32
	//: a released slot first.
	if n := len(pl.free); n > 0 {
		slot = pl.free[n-1]
		pl.free = pl.free[:n-1]
	} else {
		slot = int32(len(pl.conns))
		pl.conns = append(pl.conns, &portableConn{})
	}
	prev := pl.conns[slot]
	gen := prev.gen + 1
	//: zero names no connection.
	if gen == 0 {
		gen = 1
	}
	pc := &portableConn{
		conn: c, gen: gen, ack: make(chan struct{}, 1), kick: make(chan struct{}, 1), open: true, lastIO: time.Now(),
		pressure: pl.g.soft > 0 && pl.g.live() > int64(pl.g.soft),
	}
	pl.conns[slot] = pc
	pl.accepted.Add(1)
	pl.readers.Add(1)
	go pl.reader(slot, pc)
	go pl.writer(slot, pc)
	pl.h.OnOpen(pl, corenet.LoopConnRef{Slot: uint32(slot), Gen: gen})
}

// reader reads pc into one buffer, reused once the handler's batch ended.
func (pl *portableLoop) reader(slot int32, pc *portableConn) {
	defer pl.readers.Done()
	buf := make([]byte, portableReadSize)
	//: until the connection ends.
	for {
		n, err := pc.conn.Read(buf)
		//: bytes first, then the end.
		if n > 0 {
			pl.events <- portableEvent{kind: pevData, slot: slot, gen: pc.gen, data: buf[:n]}
			<-pc.ack
			//: a test marks the buffer the batch released, so a byte kept past
			//: OnBatchEnd reads as the mark rather than as the next bytes.
			if markRecycled.Load() {
				fillMark(buf[:n])
			}
		}
		//: the end of input, or a failure.
		if err != nil {
			pl.events <- portableEvent{kind: pevEnd, slot: slot, gen: pc.gen, err: err}
			//: done.
			return
		}
	}
}

// writer sends pc's output, one flight at a time.
func (pl *portableLoop) writer(slot int32, pc *portableConn) {
	//: until closing with nothing left, or a failure.
	for range pc.kick {
		//: everything queued, then the close if asked.
		for {
			pc.mu.Lock()
			out := pc.out
			pc.flight = len(out)
			pc.out = nil
			closing := pc.closing
			pc.mu.Unlock()
			//: nothing to send.
			if len(out) == 0 {
				//: a close waits only for the output.
				if closing {
					swallowErr(pc.conn.Close())
					//: done.
					return
				}
				break
			}
			_, err := pc.conn.Write(out)
			pc.mu.Lock()
			pc.flight = 0
			pc.mu.Unlock()
			//: the socket failed: the reader reports the end.
			if err != nil {
				swallowErr(pc.conn.Close())
				//: done.
				return
			}
			pl.events <- portableEvent{kind: pevSent, slot: slot, gen: pc.gen}
		}
	}
}

// lookupSlot returns slot's connection when gen is still its generation.
func (pl *portableLoop) lookupSlot(slot int32, gen uint32) *portableConn {
	//: a slot never reached, or another generation.
	if int(slot) >= len(pl.conns) || pl.conns[slot].gen != gen || gen == 0 {
		//: stale.
		return nil
	}
	//: live or finishing.
	return pl.conns[slot]
}

// lookup resolves a reference to an open connection.
func (pl *portableLoop) lookup(ref corenet.LoopConnRef) (int32, *portableConn) {
	pc := pl.lookupSlot(int32(ref.Slot), ref.Gen)
	//: closed, closing, or never.
	if ref.Ring != 0 || pc == nil || !pc.open || pc.reasonSet {
		//: stale.
		return 0, nil
	}
	//: live.
	return int32(ref.Slot), pc
}

// heard records bytes received at now; the first ones mark the connection
// heard, so the drain may close it once settled (ADR 0196).
func (pc *portableConn) heard(now time.Time) {
	pc.lastIO = now
	//: the first byte.
	if pc.heardAt.IsZero() {
		pc.heardAt = now
	}
}

// deliver hands data to the handler behind any spill.
func (pl *portableLoop) deliver(slot int32, pc *portableConn, data []byte) {
	pc.heard(time.Now())
	ref := corenet.LoopConnRef{Slot: uint32(slot), Gen: pc.gen}
	buf := data
	//: behind a spill the bytes join it; appending never touches the bytes
	//: already handed out, and a reallocation leaves the old array to them.
	if len(pc.spill) > pc.spillOff {
		//: the cap bounds what is unconsumed.
		if len(pc.spill)-pc.spillOff+len(data) > pl.spillCap {
			pl.finish(slot, pc, corenet.LoopSpillFull)
			//: closed.
			return
		}
		pc.spill = append(pc.spill, data...)
		buf = pc.spill[pc.spillOff:]
	}
	n := clampUsed(pl.h.OnData(pl, ref, buf), len(buf))
	//: closed from inside the callback.
	if !pc.open {
		//: done.
		return
	}
	//: consumed from the spill, or a new tail kept.
	if len(pc.spill) > pc.spillOff {
		pc.spillOff += n
		//: a head consumed, listed once for the batch's end.
		if n > 0 && !pc.compact {
			pc.compact = true
			pl.compact = append(pl.compact, slot)
		}
	} else if n < len(buf) {
		tail := buf[n:]
		//: past the cap.
		if len(tail) > pl.spillCap {
			pl.finish(slot, pc, corenet.LoopSpillFull)
			//: closed.
			return
		}
		pc.spill = append(pc.spill[:0:0], tail...)
		pc.spillOff = 0
	}
}

// clampUsed treats an OnData return outside 0 to n as n.
func clampUsed(v, n int) int {
	//: out of range means everything.
	if v < 0 || v > n {
		//: all of it.
		return n
	}
	//: as returned.
	return v
}

// pending is how many output bytes pc holds.
func (pl *portableLoop) pending(pc *portableConn) int {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	//: queued and in flight.
	return len(pc.out) + pc.flight
}

// markDirty lists slot for the end of the batch.
func (pl *portableLoop) markDirty(slot int32) {
	pl.dirty = append(pl.dirty, slot)
}

// finish calls OnClose and closes the connection; the slot is free at once,
// its generation moved on, so nothing of it is reachable any more.
func (pl *portableLoop) finish(slot int32, pc *portableConn, err error) {
	//: once.
	if !pc.open {
		//: done.
		return
	}
	pc.open = false
	//: a reader's clean end is a clean close.
	if errors.Is(err, io.EOF) || errors.Is(err, stdnet.ErrClosed) {
		err = nil
	}
	//: a reason chosen earlier — the drain, a spill — wins.
	if pc.reasonSet {
		err = pc.closeErr
	}
	pl.live.Add(-1)
	pl.closed.Add(1)
	pl.h.OnClose(pl, corenet.LoopConnRef{Slot: uint32(slot), Gen: pc.gen}, err)
	swallowErr(pc.conn.Close())
	close(pc.kick)
	pl.free = append(pl.free, slot)
}

// timers fires deadlines and closes idle connections, and sets the next scan
// a tick later, or at the earliest deadline still to come when it is sooner:
// a deadline OnTimeout sets on any connection lowers it as SetDeadline does.
func (pl *portableLoop) timers(now time.Time) {
	pl.timersAt = now.Add(portableTick)
	//: every open connection; the portable backend pays a scan per tick.
	for slot, pc := range pl.conns {
		//: open ones only.
		if pc.open {
			pl.timer(int32(slot), pc, now)
		}
	}
}

// timer closes pc when it is idle past the bound with nothing to send, fires
// its deadline when it came, and brings the next scan forward to a deadline
// still to come before it.
func (pl *portableLoop) timer(slot int32, pc *portableConn, now time.Time) {
	//: idle past the bound with nothing to send.
	if pl.idle > 0 && now.Sub(pc.lastIO) >= pl.idle && pl.pending(pc) == 0 {
		pc.reasonSet, pc.closeErr = true, os.ErrDeadlineExceeded
		pl.finish(slot, pc, os.ErrDeadlineExceeded)
		//: closed.
		return
	}
	//: the handler's deadline.
	if !pc.deadline.IsZero() && !now.Before(pc.deadline) {
		pc.deadline = time.Time{}
		pl.h.OnTimeout(pl, corenet.LoopConnRef{Slot: uint32(slot), Gen: pc.gen})
	}
	//: a deadline still to come may come before the tick.
	if !pc.deadline.IsZero() && pc.deadline.Before(pl.timersAt) {
		pl.timersAt = pc.deadline
	}
}

// control applies the drain.
func (pl *portableLoop) control() {
	//: the drain closes every connection with no partial input.
	if !pl.g.drainReq.Load() {
		//: not draining.
		return
	}
	pl.draining = true
	pl.closeSettled()
}

// closeSettled marks for closing, as the drain does, every open connection
// the drain may close now (drainable).
func (pl *portableLoop) closeSettled() {
	now := time.Now()
	//: each open one the drain may close.
	for slot, pc := range pl.conns {
		//: mid-request ones, ones waiting on a worker, fresh ones not yet
		//: heard nor quiet: they finish first.
		if pl.drainable(int32(slot), pc, now) {
			pc.reasonSet, pc.closeErr = true, corenet.ServerClosed
			pl.markDirty(int32(slot))
		}
	}
}

// drainable reports whether the drain closes pc, at slot, now: open with
// nothing unconsumed, no answer out with a worker (R3), and either it has
// sent something or it stayed silent for portableQuiet.
func (pl *portableLoop) drainable(slot int32, pc *portableConn, now time.Time) bool {
	//: settled, heard or quiet, and waiting on nobody.
	return pc.open && !pc.reasonSet && len(pc.spill) == pc.spillOff &&
		(!pc.heardAt.IsZero() || now.Sub(pc.lastIO) >= portableQuiet) &&
		(pl.busy == nil || !pl.busy(pl.lane, corenet.LoopConnRef{Slot: uint32(slot), Gen: pc.gen}))
}

// endBatch flushes the dirty connections, settles the closes, releases the
// batch's buffers and compacts the spills.
func (pl *portableLoop) endBatch() {
	//: every dirty connection, once.
	for _, slot := range pl.dirty {
		pl.settle(slot)
	}
	pl.dirty = pl.dirty[:0]
	//: the readers may reuse their buffers.
	for _, a := range pl.batchAcks {
		a <- struct{}{}
	}
	pl.batchAcks = pl.batchAcks[:0]
	pl.compactListed()
	pl.batches.Add(1)
}

// settle finishes a dirty connection that is closing with nothing left to
// send, and otherwise kicks its writer.
func (pl *portableLoop) settle(slot int32) {
	pc := pl.conns[slot]
	//: closed in the batch.
	if !pc.open {
		//: nothing to settle.
		return
	}
	pc.mu.Lock()
	empty := len(pc.out) == 0 && pc.flight == 0
	//: a close waits for the output; the writer closes after it.
	if pc.reasonSet || pc.peerDone {
		pc.closing = true
	}
	pc.mu.Unlock()
	//: nothing left and closing: finished now.
	if empty && (pc.reasonSet || pc.peerDone) {
		pl.finish(slot, pc, pc.closeErr)
		//: finished.
		return
	}
	//: the writer takes it from here.
	select {
	case pc.kick <- struct{}{}:
	default:
	}
}

// compactListed moves the consumed head of every spill listed in the batch
// out, now that no handed-out buffer survives, so the batch's end costs the
// connections it touched, not the open ones. A slot taken again since its
// listing holds a new connection, compacted harmlessly.
func (pl *portableLoop) compactListed() {
	//: each listed slot, then the list is empty for the next batch.
	for _, slot := range pl.compact {
		pc := pl.conns[slot]
		pc.compact = false
		//: a consumed head moves out.
		if pc.open && pc.spillOff > 0 {
			pc.spill = append(pc.spill[:0], pc.spill[pc.spillOff:]...)
			pc.spillOff = 0
		}
	}
	pl.compact = pl.compact[:0]
}

// closeAll closes every connection as the backend stops.
//
// Goroutine lifecycle: it launches one drainer that takes the last events a
// reader or a handshake may still send — closing the connections they hand
// over, which no lane will open — and returns after one second with none and
// no handshake still running, the buffer emptied.
func (pl *portableLoop) closeAll() {
	//: each open one.
	for slot, pc := range pl.conns {
		//: still open.
		if pc.open {
			pc.reasonSet, pc.closeErr = true, corenet.ServerClosed
			pl.finish(int32(slot), pc, corenet.ServerClosed)
		}
	}
	pl.postMu.Lock()
	pl.postShut = true
	pl.postMu.Unlock()
	//: the readers blocked on their batch are released, then they end on
	//: their closed sockets.
	for _, a := range pl.batchAcks {
		a <- struct{}{}
	}
	go func() {
		quiet := time.NewTimer(time.Second)
		defer quiet.Stop()
		//: a reader still sending its last event finds nobody; drained here,
		//: until a second passes with none and no handshake still running —
		//: one that completes hands its connection over to this loop alone.
		for {
			select {
			case ev := <-pl.events:
				pl.discard(&ev)
				quiet.Reset(time.Second)
			case <-quiet.C:
				//: a handshake still running may yet hand one over.
				if pl.negotiating.Load() > 0 {
					quiet.Reset(time.Second)
					continue
				}
				//: nobody left to send: what the last ones left, then out.
				pl.discardBuffered()
				return
			}
		}
	}()
}

// discard drops an event that reached a stopped lane: a connection handed
// over and never opened is closed, and uncounted, as negotiate's own stopped
// branch does; any other event is about a connection closeAll finished.
func (pl *portableLoop) discard(ev *portableEvent) {
	//: never registered: closeAll could not have closed it.
	if ev.kind == pevOpen {
		pl.live.Add(-1)
		swallowErr(ev.conn.Close())
	}
}

// discardBuffered discards, without waiting, the events still buffered.
func (pl *portableLoop) discardBuffered() {
	//: until the buffer is empty.
	for {
		select {
		case ev := <-pl.events:
			pl.discard(&ev)
		default:
			//: empty.
			return
		}
	}
}

// drainPosts runs the posted operations.
func (pl *portableLoop) drainPosts() {
	pl.postWait.Store(false)
	//: popped one at a time, run unlocked.
	for {
		pl.postMu.Lock()
		//: empty.
		if pl.postN == 0 {
			pl.postMu.Unlock()
			//: done.
			return
		}
		op, arg := pl.postOps[pl.postHead], pl.postArgs[pl.postHead]
		pl.postOps[pl.postHead] = nil
		pl.postHead = (pl.postHead + 1) % len(pl.postOps)
		pl.postN--
		pl.postMu.Unlock()
		op(pl, arg)
	}
}

// Write implements corenet.LoopIO.
func (pl *portableLoop) Write(ref corenet.LoopConnRef, p []byte) error {
	slot, pc := pl.lookup(ref)
	//: closed.
	if pc == nil {
		//: stale.
		return corenet.LoopStaleRef
	}
	pc.mu.Lock()
	//: the ceiling counts what is in flight too.
	if len(pc.out)+pc.flight+len(p) > pl.outCap {
		pc.mu.Unlock()
		pc.wantWrite = true
		//: refused.
		return corenet.LoopOutputFull
	}
	pc.out = append(pc.out, p...)
	pc.mu.Unlock()
	pl.markDirty(slot)
	//: queued.
	return nil
}

// Close implements corenet.LoopIO.
func (pl *portableLoop) Close(ref corenet.LoopConnRef) error {
	slot, pc := pl.lookup(ref)
	//: already closing or closed.
	if pc == nil {
		//: stale.
		return corenet.LoopStaleRef
	}
	pc.reasonSet, pc.closeErr = true, nil
	pl.markDirty(slot)
	//: finished once the output left.
	return nil
}

// SetDeadline implements corenet.LoopIO.
func (pl *portableLoop) SetDeadline(ref corenet.LoopConnRef, d time.Duration) error {
	_, pc := pl.lookup(ref)
	//: closed.
	if pc == nil {
		//: stale.
		return corenet.LoopStaleRef
	}
	//: zero or less disarms.
	if d <= 0 {
		pc.deadline = time.Time{}
	} else {
		pc.deadline = time.Now().Add(d)
		//: due before the next scan: the scan comes sooner.
		if pc.deadline.Before(pl.timersAt) {
			pl.timersAt = pc.deadline
		}
	}
	//: armed.
	return nil
}

// Pressure implements corenet.LoopIO.
func (pl *portableLoop) Pressure(ref corenet.LoopConnRef) bool {
	_, pc := pl.lookup(ref)
	//: a stale reference is under none.
	return pc != nil && pc.pressure
}

// Loop implements corenet.LoopIO: the lane's index.
func (pl *portableLoop) Loop() int {
	//: one handler goroutine a lane.
	return pl.lane
}

// Post implements corenet.LoopIO.
func (pl *portableLoop) Post(op corenet.PostFunc, arg uintptr) error {
	pl.postMu.Lock()
	//: a stopped backend runs nothing.
	if pl.postShut {
		pl.postMu.Unlock()
		//: gone.
		return corenet.ServerClosed
	}
	//: full.
	if pl.postN == len(pl.postOps) {
		pl.postMu.Unlock()
		//: refused.
		return corenet.LoopPostQueueFull
	}
	i := (pl.postHead + pl.postN) % len(pl.postOps)
	pl.postOps[i], pl.postArgs[i] = op, arg
	pl.postN++
	pl.postMu.Unlock()
	//: one wake per drain.
	if pl.postWait.CompareAndSwap(false, true) {
		select {
		case pl.wake <- struct{}{}:
		default:
		}
	}
	//: queued.
	return nil
}

// portableEngine is the portable backend as server drives it: the lifecycle
// half, apart from the LoopIO each lane's handler is called with, whose
// Close names a connection.
type portableEngine struct {
	g *portableGroup
}

// wakeAll rouses every lane's handler goroutine.
func (g *portableGroup) wakeAll() {
	//: one wake a lane, never blocking.
	for _, pl := range g.lanes {
		select {
		case pl.wake <- struct{}{}:
		default:
		}
	}
}

// Shutdown implements loopEngine.
func (e portableEngine) Shutdown(ctx context.Context) error {
	g := e.g
	g.drainReq.Store(true)
	g.wakeAll()
	g.takeQueues(ctx)
	//: every lane drained, or out of budget.
	for _, pl := range g.lanes {
		select {
		case <-pl.done:
		case <-ctx.Done():
			swallowErr(e.Close())
			//: cut short.
			return errs.Wrap(corenet.DrainTimeout, errs.WrapParams{}, errs.String("group", g.group))
		}
	}
	//: drained.
	return nil
}

// Close implements loopEngine.
func (e portableEngine) Close() error {
	g := e.g
	g.once.Do(func() {
		g.stopReq.Store(true)
		g.closeOwned()
		g.wakeAll()
		//: every lane's goroutine returned.
		for _, pl := range g.lanes {
			<-pl.done
		}
	})
	//: closed.
	return nil
}

// State implements loopEngine.
func (e portableEngine) State() corenet.LoopStateValue {
	g := e.g
	rings := make([]corenet.LoopRingStateValue, 0, len(g.lanes))
	//: one ring a lane, on the Go heap.
	for _, pl := range g.lanes {
		rings = append(rings, corenet.LoopRingStateValue{
			Conns: pl.live.Load(), Accepted: pl.accepted.Load(), Closed: pl.closed.Load(),
			Batches: pl.batches.Load(), Evicted: 0,
		})
	}
	//: as many loops as lanes, one ring each.
	return corenet.LoopStateValue{
		Group: g.group, Backend: corenet.LoopBackendPortable, ProbeStage: "portable",
		Degraded: g.degraded != "", DegradedReason: g.degraded,
		Loops: len(g.lanes), LoopsSource: g.loopsSource, Rings: 1, RingsSource: "single", GOMAXPROCS: g.procs,
		Capabilities: slices.Clone(g.caps),
		RingStates:   rings,
	}
}

// fillMark fills b with recycledMark.
func fillMark(b []byte) {
	//: every byte the batch held.
	for i := range b {
		b[i] = recycledMark
	}
}
