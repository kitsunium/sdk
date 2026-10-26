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
)

// The kinds of a portable event.
const (
	pevOpen uint8 = iota
	pevData
	pevEnd
	pevSent
)

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
	open      bool
	spill     []byte
	spillOff  int
	lastIO    time.Time
	deadline  time.Time
	pressure  bool
	wantWrite bool
	peerDone  bool
	closeErr  error
	reasonSet bool
	// compact lists the connection on its loop's compaction list.
	compact bool
}

// portableLoop is the portable backend (ADR 0186 §D3, §D7): the default
// engine's accept path and a goroutine pair per connection on the netpoller,
// every handler callback serialised onto one handler goroutine, so the
// LoopHandler contract holds unchanged: one goroutine calls it, buf valid
// until OnBatchEnd.
type portableLoop struct {
	group    string
	h        corenet.LoopHandler
	spillCap int
	outCap   int
	soft     int
	maxConns int
	idle     time.Duration
	degraded string
	// caps is the group's capability report: every row unprobed, or
	// disabled; noDelayOff is tcp-nodelay switched off, so each accepted
	// connection has the netpoller's TCP_NODELAY cleared (ADR 0189).
	caps       []corenet.CapabilityValue
	noDelayOff bool

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

	drainReq atomic.Bool
	stopReq  atomic.Bool
	draining bool
	done     chan struct{}
	lns      []stdnet.Listener
	accepted atomic.Uint64
	closed   atomic.Uint64
	batches  atomic.Uint64
	rejected atomic.Uint64
	once     sync.Once
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

// startPortable starts the portable backend on lns. degraded names why a
// Linux auto group fell back to it, empty otherwise.
//
// Goroutine lifecycle: it launches one acceptor per listener, which returns
// once server closes that listener or the backend stops, and the handler
// goroutine, which returns once the backend stopped or drained to its last
// connection, closing done as it does.
func startPortable(group *StreamGroup, lns []stdnet.Listener, degraded string, plan loopPlan) portableEngine {
	v := group.loop
	pl := &portableLoop{
		group: group.name, h: group.loopHandler,
		spillCap: orDefaultInt(v.SpillCap, portableSpillCap),
		outCap:   orDefaultInt(v.OutputCap, portableOutputCap),
		soft:     max(v.SoftConns, 0), maxConns: max(group.limits.MaxConns, 0),
		idle: loopIdle(group), degraded: degraded,
		events:  make(chan portableEvent, portableEvents),
		postOps: make([]corenet.PostFunc, portablePostCap), postArgs: make([]uintptr, portablePostCap),
		wake: make(chan struct{}, 1), done: make(chan struct{}), lns: lns,
		caps:       append(loop.Unprobed("portable", plan.disabled, plan.disabledBy), portableNoDelay(plan)),
		noDelayOff: slices.Contains(plan.disabled, corenet.CapabilityTCPNoDelay),
	}
	//: one acceptor per listener; it ends when server closes the listener.
	for _, ln := range lns {
		go pl.acceptLoop(ln)
	}
	go pl.run(pl.done)
	//: serving.
	return portableEngine{pl: pl}
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

// acceptLoop hands each accepted connection to the handler goroutine.
func (pl *portableLoop) acceptLoop(ln stdnet.Listener) {
	failures := 0
	//: until server closes the listener.
	for {
		c, err := ln.Accept()
		//: closed, or a failure to back off from.
		if err != nil {
			//: server closed it.
			if errors.Is(err, stdnet.ErrClosed) || pl.stopReq.Load() {
				//: done.
				return
			}
			failures++
			time.Sleep(acceptDelay(failures))
			continue
		}
		failures = 0
		//: MaxConns: accepted and closed, a refusal the peer sees.
		if pl.maxConns > 0 && pl.live.Load() >= int64(pl.maxConns) || pl.drainReq.Load() {
			pl.rejected.Add(1)
			swallowErr(c.Close())
			continue
		}
		pl.live.Add(1)
		pl.events <- portableEvent{kind: pevOpen, conn: c}
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
		if pl.stopReq.Load() || (pl.draining && pl.live.Load() == 0) {
			pl.closeAll()
			//: out.
			return
		}
	}
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
	if tc, ok := c.(*stdnet.TCPConn); ok && pl.noDelayOff {
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
		pressure: pl.soft > 0 && pl.live.Load() > int64(pl.soft),
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

// deliver hands data to the handler behind any spill.
func (pl *portableLoop) deliver(slot int32, pc *portableConn, data []byte) {
	pc.lastIO = time.Now()
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
	if !pl.drainReq.Load() {
		//: not draining.
		return
	}
	pl.draining = true
	//: each open one with nothing unconsumed.
	for slot, pc := range pl.conns {
		//: mid-request ones finish first.
		if pc.open && !pc.reasonSet && len(pc.spill) == pc.spillOff {
			pc.reasonSet, pc.closeErr = true, corenet.ServerClosed
			pl.markDirty(int32(slot))
		}
	}
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
// reader may still send, and returns after one second with none.
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
		//: until a second passes with none.
		for {
			select {
			case <-pl.events:
				quiet.Reset(time.Second)
			case <-quiet.C:
				return
			}
		}
	}()
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

// Loop implements corenet.LoopIO: the portable backend is one loop, index 0.
func (pl *portableLoop) Loop() int {
	//: one handler goroutine, so one loop.
	return 0
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
// half, apart from the LoopIO its handler is called with, whose Close names a
// connection.
type portableEngine struct {
	pl *portableLoop
}

// Shutdown implements loopEngine.
func (e portableEngine) Shutdown(ctx context.Context) error {
	pl := e.pl
	pl.drainReq.Store(true)
	select {
	case pl.wake <- struct{}{}:
	default:
	}
	//: drained, or out of budget.
	select {
	case <-pl.done:
		//: drained.
		return nil
	case <-ctx.Done():
		swallowErr(e.Close())
		//: cut short.
		return errs.Wrap(corenet.DrainTimeout, errs.WrapParams{}, errs.String("group", pl.group))
	}
}

// Close implements loopEngine.
func (e portableEngine) Close() error {
	pl := e.pl
	pl.once.Do(func() {
		pl.stopReq.Store(true)
		select {
		case pl.wake <- struct{}{}:
		default:
		}
		<-pl.done
	})
	//: closed.
	return nil
}

// State implements loopEngine.
func (e portableEngine) State() corenet.LoopStateValue {
	pl := e.pl
	//: one loop of one ring, on the Go heap.
	return corenet.LoopStateValue{
		Group: pl.group, Backend: corenet.LoopBackendPortable, ProbeStage: "portable",
		Degraded: pl.degraded != "", DegradedReason: pl.degraded,
		Loops: 1, Rings: 1, RingsSource: "single", GOMAXPROCS: runtime.GOMAXPROCS(0),
		Capabilities: slices.Clone(pl.caps),
		RingStates: []corenet.LoopRingStateValue{{
			Conns: pl.live.Load(), Accepted: pl.accepted.Load(), Closed: pl.closed.Load(),
			Batches: pl.batches.Load(), Evicted: 0,
		}},
	}
}
