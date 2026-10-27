package loop

import (
	"os"
	"sync/atomic"
	"syscall"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// lowWaterDiv sets the output low-water mark at OutputCap / lowWaterDiv:
	// a connection refused with LoopOutputFull hears OnWritable once its
	// pending output falls under it.
	lowWaterDiv int = 4
	// evictPerTick paces hard-pressure eviction: at most this many idle
	// connections closed per ring and tick.
	evictPerTick int = 16
)

// driver is what a backend does for the core: send a connection's output and
// release its descriptor. The core owns every other decision.
type driver interface {
	// flush starts or continues sending slot's output. It is called on the
	// loop's thread, once the batch's callbacks have returned.
	flush(r *ring, slot int32)
	// shutdown releases slot's descriptor once OnClose has been called; the
	// backend calls r.release(slot) when nothing of the connection remains in
	// the kernel.
	shutdown(r *ring, slot int32)
	// unread reports whether slot's socket holds bytes not yet received, so
	// eviction never closes a connection mid-request.
	unread(r *ring, slot int32) bool
}

// shared is what every loop of one group shares: the connection bound and
// the counters it is judged on.
type shared struct {
	// live is the group's open connections, across loops.
	live atomic.Int64
	// rejected counts connections closed at accept by MaxConns.
	rejected atomic.Uint64
	// hist counts batch durations by power of two, for the p99 State
	// reports: loops write it, State reads it.
	hist [batchBuckets]atomic.Uint64
}

// ring is one descriptor shard of a loop: its slot table, its timers, its
// least-recently-used list and its counters.
type ring struct {
	// lp is the loop the ring belongs to; index its place in it.
	lp    *loop
	index int
	// conns is the slot table, grown on demand up to the loop's table size.
	conns []connCtl
	// free is a stack of released slots.
	free []int32
	// live is the ring's open connections.
	live int
	// wheel times every slot's deadline and idle bound.
	wheel *wheel
	// lruHead and lruTail bound the list of open connections, least recent
	// at the head.
	lruHead int32
	lruTail int32
	// dirtyHead starts the list of slots to flush at the end of the batch.
	dirtyHead int32
	// drv is the backend.
	drv driver
	// st is the ring's counters, published at the end of each batch.
	st  ringCounters
	pub *ringStats
	// draining is a ring that accepts nothing more.
	draining bool
}

// ringCounters are a ring's counters as its thread keeps them, plain.
type ringCounters struct {
	accepted, closed, batches, completions uint64
	starved, retries, pauses, evicted      uint64
	watchdog                               uint64
}

// ringStats are a ring's counters as State reads them, published at the
// end of each batch.
type ringStats struct {
	conns                                  atomic.Int64
	accepted, closed, batches, completions atomic.Uint64
	starved, retries, pauses, evicted      atomic.Uint64
	watchdog                               atomic.Uint64
	draining                               atomic.Bool
}

// loop is one loop: a thread, its rings, its slab, and the LoopIO its
// handler is called with. Everything but post is touched by the loop's
// thread alone.
type loop struct {
	// index is the loop's place in its group.
	index int
	// sz is the group's resolved configuration; idle its idle bound in
	// ticks, zero for none; evictIdle the idle age eviction waits for.
	sz        sizing
	idle      int64
	evictIdle int64
	// h is the handler.
	h corenet.LoopHandler
	// grp is what the group's loops share.
	grp *shared
	// rings are the loop's rings.
	rings []*ring
	// slab holds every spill and output byte of the loop.
	slab *slab
	// post is the loop's queue of posted operations.
	post *postQueue
	// deferred are chunks released once OnBatchEnd has returned, since a
	// buffer handed to OnData stays valid until then.
	deferred []chunkRef
	// tick is the loop's clock, in ticks since start.
	tick  int64
	epoch time.Time
	// threadOK reports whether the caller runs on the loop's thread; nil when
	// the check is not compiled (the loopdebug build tag).
	threadOK func() bool
	// draining refuses new connections and closes idle ones.
	draining bool
	// hard is the loop's hard pressure: out of descriptors, or at MaxConns.
	hard bool
	// lowWater is the output mark under which OnWritable fires.
	lowWater int
}

// newRing builds ring i of lp over drv.
func newRing(lp *loop, i int, drv driver) *ring {
	//: the table starts empty and grows with the connections.
	return &ring{
		lp: lp, index: i, drv: drv,
		wheel:   newWheel(0, lp.tick),
		lruHead: noLink, lruTail: noLink, dirtyHead: noLink,
		pub: &ringStats{},
	}
}

// take reserves a slot for a new connection on descriptor fd. It reports
// false when the ring's table is full; the caller closes fd. The backend
// registers the slot, then calls opened.
func (r *ring) take(fd int32) (int32, bool) {
	var slot int32
	//: a released slot first, so the table grows only with the peak.
	if n := len(r.free); n > 0 {
		slot = r.free[n-1]
		r.free = r.free[:n-1]
	} else {
		//: the table never passes the descriptor limit it was sized by.
		if len(r.conns) >= r.lp.sz.tableSize {
			//: full.
			return 0, false
		}
		slot = int32(len(r.conns))
		r.conns = append(r.conns, emptyConn(0))
		r.wheel.grow(len(r.conns))
	}
	c := &r.conns[slot]
	gen := c.gen + 1
	//: zero names no connection.
	if gen == 0 {
		gen = 1
	}
	*c = emptyConn(gen)
	c.state, c.fd, c.lastIO = stOpen, fd, r.lp.tick
	//: reserved.
	return slot, true
}

// opened counts the connection taken on slot, flags its pressure, arms its
// timers and calls OnOpen.
func (r *ring) opened(slot int32) {
	c := &r.conns[slot]
	live := r.lp.grp.live.Add(1)
	r.live++
	r.st.accepted++
	//: past the soft bound the connection carries pressure from its accept on.
	if soft := r.lp.sz.softConns; soft > 0 && live > int64(soft) {
		c.flags |= fPressure
	}
	r.lruPush(slot)
	r.armTimer(slot)
	r.lp.h.OnOpen(r.lp, r.ref(slot))
}

// abandon returns a slot taken for a connection that never opened.
func (r *ring) abandon(slot int32) {
	c := &r.conns[slot]
	*c = emptyConn(c.gen)
	r.free = append(r.free, slot)
}

// ref is slot's current reference.
func (r *ring) ref(slot int32) corenet.LoopConnRef {
	//: ring, slot, generation.
	return corenet.LoopConnRef{Ring: uint32(r.index), Slot: uint32(slot), Gen: r.conns[slot].gen}
}

// deliver hands data, received on slot, to the handler behind any spill, and
// keeps what it does not consume. data must stay valid until OnBatchEnd
// returns, as the handler's buf does.
func (r *ring) deliver(slot int32, data []byte) {
	c := &r.conns[slot]
	//: a closing or dead connection reads nothing more.
	if c.state != stOpen || len(data) == 0 {
		//: dropped.
		return
	}
	c.lastIO = r.lp.tick
	c.flags |= fHeard
	r.lruTouch(slot)
	lp := r.lp
	//: no spill: the handler reads the received bytes in place.
	if c.spill.empty() {
		n := clampConsumed(lp.h.OnData(lp, r.ref(slot), data), len(data))
		//: the handler may have closed it from inside.
		if c.state == stOpen && n < len(data) {
			r.keep(slot, data[n:])
		}
		//: delivered.
		return
	}
	//: behind a spill the bytes join it, so the handler sees one run.
	if !r.appendSpill(slot, data) {
		//: past the ceiling: the connection was closed.
		return
	}
	buf := lp.slab.data(c.spill)
	n := clampConsumed(lp.h.OnData(lp, r.ref(slot), buf), len(buf))
	c = &r.conns[slot]
	c.spill.off += int32(n)
	//: fully consumed: the chunk goes back once the batch is over.
	if c.spill.off == c.spill.len {
		lp.deferred = append(lp.deferred, c.spill)
		c.spill = chunkRef{idx: noChunk}
	}
}

// keep copies an unconsumed tail into a fresh spill.
func (r *ring) keep(slot int32, tail []byte) {
	lp := r.lp
	//: one connection may hold no more than its cap.
	if len(tail) > lp.sz.spillCap {
		r.fail(slot, rSpill, 0)
		//: closed.
		return
	}
	ch, ok := lp.slab.alloc(len(tail))
	//: the loop's budget is spent: load is shed by name.
	if !ok {
		r.fail(slot, rSpill, 0)
		//: closed.
		return
	}
	ch.len = int32(copy(lp.slab.bytes(ch), tail))
	r.conns[slot].spill = ch
}

// appendSpill adds data behind slot's spill. The spill's bytes already handed
// to the handler stay where they are until OnBatchEnd: a spill too small is
// copied to a larger chunk and the old one released after the batch, never
// compacted or reallocated in place.
func (r *ring) appendSpill(slot int32, data []byte) bool {
	lp := r.lp
	c := &r.conns[slot]
	held := int(c.spill.len - c.spill.off)
	//: the cap bounds what is unconsumed, not what was ever received.
	if held+len(data) > lp.sz.spillCap {
		r.fail(slot, rSpill, 0)
		//: closed.
		return false
	}
	//: room behind the data: appended past every byte the handler saw.
	if lp.slab.size(c.spill)-int(c.spill.len) >= len(data) {
		c.spill.len += int32(copy(lp.slab.bytes(c.spill)[c.spill.len:], data))
		//: appended.
		return true
	}
	ch, ok := lp.slab.alloc(held + len(data))
	//: the loop's budget is spent.
	if !ok {
		r.fail(slot, rSpill, 0)
		//: closed.
		return false
	}
	dst := lp.slab.bytes(ch)
	n := copy(dst, lp.slab.data(c.spill))
	n += copy(dst[n:], data)
	ch.len = int32(n)
	lp.deferred = append(lp.deferred, c.spill)
	c.spill = ch
	//: moved to a larger chunk.
	return true
}

// spillRoom returns the free bytes behind slot's spill, growing it to a chunk
// with at least want free bytes within the cap, so a backend can read
// straight into it. It returns nil when the spill is at its cap or the budget
// is spent; the caller then closes or reads elsewhere.
func (r *ring) spillRoom(slot int32, want int) []byte {
	lp := r.lp
	c := &r.conns[slot]
	held := int(c.spill.len - c.spill.off)
	room := min(want, lp.sz.spillCap-held)
	//: at the cap: nothing more may be read.
	if room <= 0 {
		//: no room.
		return nil
	}
	free := lp.slab.size(c.spill) - int(c.spill.len)
	//: the chunk has room already.
	if free >= room {
		//: the tail of the chunk.
		return lp.slab.bytes(c.spill)[c.spill.len : int(c.spill.len)+room]
	}
	ch, ok := lp.slab.alloc(held + room)
	//: the budget is spent.
	if !ok {
		//: no room.
		return nil
	}
	ch.len = int32(copy(lp.slab.bytes(ch), lp.slab.data(c.spill)))
	lp.deferred = append(lp.deferred, c.spill)
	c.spill = ch
	//: the tail of the new chunk.
	return lp.slab.bytes(ch)[ch.len : int(ch.len)+room]
}

// deliverSpill hands n bytes read straight into slot's spill (spillRoom) to
// the handler with the rest of the spill.
func (r *ring) deliverSpill(slot int32, n int) {
	c := &r.conns[slot]
	c.spill.len += int32(n)
	c.lastIO = r.lp.tick
	c.flags |= fHeard
	r.lruTouch(slot)
	lp := r.lp
	buf := lp.slab.data(c.spill)
	used := clampConsumed(lp.h.OnData(lp, r.ref(slot), buf), len(buf))
	c = &r.conns[slot]
	c.spill.off += int32(used)
	//: fully consumed: released once the batch is over.
	if c.spill.off == c.spill.len {
		lp.deferred = append(lp.deferred, c.spill)
		c.spill = chunkRef{idx: noChunk}
	}
}

// hasSpill reports whether slot holds unconsumed input.
func (r *ring) hasSpill(slot int32) bool {
	//: any chunk is unconsumed input.
	return !r.conns[slot].spill.empty()
}

// compactSpill moves slot's spill's unconsumed bytes to the front of its chunk.
// It runs after OnBatchEnd, when no handed-out buffer survives.
func (r *ring) compactSpill(slot int32) {
	c := &r.conns[slot]
	//: nothing to move.
	if c.spill.empty() || c.spill.off == 0 {
		//: done.
		return
	}
	b := r.lp.slab.bytes(c.spill)
	n := copy(b, b[c.spill.off:c.spill.len])
	c.spill.off, c.spill.len = 0, int32(n)
}

// clampConsumed treats an OnData return outside 0 to n as n, as LoopHandler
// documents.
func clampConsumed(v, n int) int {
	//: out of range means everything.
	if v < 0 || v > n {
		//: all of it.
		return n
	}
	//: as returned.
	return v
}

// markDirty puts slot on the list flushed at the end of the batch.
func (r *ring) markDirty(slot int32) {
	c := &r.conns[slot]
	//: listed once.
	if c.flags&fDirty != 0 {
		//: already listed.
		return
	}
	c.flags |= fDirty
	c.dirtyNext = r.dirtyHead
	r.dirtyHead = slot
}

// flushDirty sends every dirty slot's output and finishes the closes it
// waited for. It runs once OnBatchEnd has returned.
func (r *ring) flushDirty() {
	//: the list is taken whole; a flush that dirties a slot lists it again.
	for s := r.dirtyHead; s != noLink; {
		r.dirtyHead = noLink
		//: walked from the snapshot.
		for s != noLink {
			c := &r.conns[s]
			nxt := c.dirtyNext
			c.flags &^= fDirty
			c.dirtyNext = noLink
			r.settle(s)
			s = nxt
		}
		s = r.dirtyHead
	}
}

// settle flushes slot's output and finishes it when it was waiting to close
// and has nothing left to send.
func (r *ring) settle(slot int32) {
	c := &r.conns[slot]
	//: a dead slot has nothing to flush.
	if c.state != stOpen && c.state != stClosing {
		//: done.
		return
	}
	//: output to send.
	if c.pending() > 0 {
		r.drv.flush(r, slot)
		c = &r.conns[slot]
	}
	//: a close, or a peer that finished, waits only for the output.
	if (c.state == stClosing || c.flags&fPeerDone != 0) && c.pending() == 0 && c.state != stDead {
		r.finish(slot, c.reason, c.errno)
	}
}

// sent records that some of slot's output was sent, and fires OnWritable when
// a producer waits and the output fell under the low-water mark. The byte
// count is the driver's to pass; the pending total already reflects it.
func (r *ring) sent(slot int32, _ int) {
	c := &r.conns[slot]
	c.lastIO = r.lp.tick
	//: someone waits for room.
	if c.flags&fWantWritable != 0 && c.state == stOpen && c.pending() < r.lp.lowWater {
		c.flags &^= fWantWritable
		r.lp.h.OnWritable(r.lp, r.ref(slot))
	}
}

// fail closes slot now for reason, whatever output it holds.
func (r *ring) fail(slot int32, reason uint8, errno int32) {
	//: a slot already finished is left alone.
	if r.conns[slot].state == stOpen || r.conns[slot].state == stClosing {
		r.finish(slot, reason, errno)
	}
}

// finish calls OnClose, releases every chunk and timer of slot, and hands the
// descriptor to the backend; the slot returns to the free list once the
// backend calls release.
func (r *ring) finish(slot int32, reason uint8, errno int32) {
	c := &r.conns[slot]
	ref := r.ref(slot)
	c.state = stDead
	c.reason, c.errno = reason, errno
	r.wheel.disarm(slot)
	r.lruUnlink(slot)
	lp := r.lp
	lp.deferred = append(lp.deferred, c.spill, c.out)
	c.spill, c.out = chunkRef{idx: noChunk}, chunkRef{idx: noChunk}
	r.live--
	r.st.closed++
	lp.grp.live.Add(-1)
	lp.h.OnClose(lp, ref, closeErr(reason, errno))
	r.drv.shutdown(r, slot)
}

// release returns a dead slot to the free list. The backend calls it once the
// descriptor is closed and no operation of the slot remains in flight, so the
// slot's memory and generation move on only then.
func (r *ring) release(slot int32) {
	c := &r.conns[slot]
	//: the flight chunk may still have been the kernel's until now.
	r.lp.deferred = append(r.lp.deferred, c.flight)
	*c = emptyConn(c.gen)
	r.free = append(r.free, slot)
}

// closeErr maps a close reason to the preallocated error OnClose receives.
func closeErr(reason uint8, errno int32) error {
	//: one preallocated error per reason; an errno below 256 boxes without
	//: allocating.
	switch reason {
	//: past the spill ceiling.
	case rSpill:
		//: the spill sentinel.
		return corenet.LoopSpillFull
	//: idle past the bound.
	case rIdle:
		//: the deadline error the standard library uses.
		return os.ErrDeadlineExceeded
	//: the group drains.
	case rDrain:
		//: the server is closing.
		return corenet.ServerClosed
	//: evicted under hard pressure.
	case rEvict:
		//: the connection made room for another.
		return corenet.ConnLimitReached
	//: the socket failed.
	case rErrno:
		//: the kernel's own error.
		return syscall.Errno(errno)
	//: closed by the handler or the peer, cleanly.
	default:
		//: no error.
		return nil
	}
}

// armTimer arms slot's timer at the earlier of its deadline and its idle
// bound.
func (r *ring) armTimer(slot int32) {
	c := &r.conns[slot]
	at := c.deadline
	//: the idle bound counts from the last byte.
	if idle := r.lp.idle; idle > 0 {
		exp := c.lastIO + idle
		//: whichever comes first.
		if at == 0 || exp < at {
			at = exp
		}
	}
	//: nothing to time.
	if at == 0 {
		r.wheel.disarm(slot)
		//: done.
		return
	}
	r.wheel.arm(slot, at)
}

// expire is the wheel's callback: it fires the handler's deadline or closes
// an idle connection, and re-arms what is still due later.
func (r *ring) expire(slot int32) {
	c := &r.conns[slot]
	//: a slot that closed since it was found due.
	if c.state != stOpen {
		//: nothing to do.
		return
	}
	now := r.lp.tick
	//: idle past the bound, judged from the last byte, not the arming.
	if idle := r.lp.idle; idle > 0 && c.lastIO+idle <= now && c.pending() == 0 {
		r.finish(slot, rIdle, 0)
		//: closed.
		return
	}
	//: the handler's deadline came.
	if c.deadline != 0 && c.deadline <= now {
		c.deadline = 0
		r.armTimer(slot)
		r.lp.h.OnTimeout(r.lp, r.ref(slot))
		//: fired.
		return
	}
	//: re-armed at whatever is due next.
	r.armTimer(slot)
}

// lruPush appends slot as the most recent.
func (r *ring) lruPush(slot int32) {
	c := &r.conns[slot]
	c.lruPrev, c.lruNext = r.lruTail, noLink
	//: linked behind the tail, or as the only element.
	if r.lruTail != noLink {
		r.conns[r.lruTail].lruNext = slot
	} else {
		r.lruHead = slot
	}
	r.lruTail = slot
}

// lruUnlink removes slot from the list.
func (r *ring) lruUnlink(slot int32) {
	c := &r.conns[slot]
	//: the predecessor, or the head, skips the slot.
	if c.lruPrev != noLink {
		r.conns[c.lruPrev].lruNext = c.lruNext
	} else if r.lruHead == slot {
		r.lruHead = c.lruNext
	}
	//: the follower, or the tail, skips it too.
	if c.lruNext != noLink {
		r.conns[c.lruNext].lruPrev = c.lruPrev
	} else if r.lruTail == slot {
		r.lruTail = c.lruPrev
	}
	c.lruPrev, c.lruNext = noLink, noLink
}

// lruTouch moves slot to the most recent end.
func (r *ring) lruTouch(slot int32) {
	//: already the most recent.
	if r.lruTail == slot {
		//: done.
		return
	}
	r.lruUnlink(slot)
	r.lruPush(slot)
}

// evict closes up to evictPerTick connections idle for at least the eviction
// age, least recent first, never one holding unread or unconsumed bytes or
// output. It returns how many it closed.
func (r *ring) evict() int {
	lp := r.lp
	//: eviction is opt-in.
	if lp.evictIdle <= 0 {
		//: nothing evicted.
		return 0
	}
	done := 0
	s := r.lruHead
	//: the least recent first; the walk stops at the first one too recent.
	for s != noLink && done < evictPerTick {
		c := &r.conns[s]
		nxt := c.lruNext
		//: everyone behind is more recent still.
		if lp.tick-c.lastIO < lp.evictIdle {
			//: done.
			break
		}
		//: a connection mid-request, or with output, keeps its place.
		if c.state == stOpen && c.spill.empty() && c.pending() == 0 && !r.drv.unread(r, s) {
			r.st.evicted++
			r.finish(s, rEvict, 0)
			done++
		}
		s = nxt
	}
	//: how many made room.
	return done
}

// endBatch runs once OnBatchEnd has returned: flushes, releases the chunks the
// batch held, compacts spills, and publishes the counters.
func (lp *loop) endBatch(started time.Time) {
	//: every ring's output leaves in the order it was written.
	for _, r := range lp.rings {
		r.flushDirty()
	}
	//: the chunks handed to OnData are no longer anyone's.
	for _, ch := range lp.deferred {
		lp.slab.free(ch)
	}
	lp.deferred = lp.deferred[:0]
	//: a spill whose head was consumed starts again at its chunk's front.
	for _, r := range lp.rings {
		r.compactAll()
		r.st.batches++
		r.publish()
	}
	lp.grp.observe(time.Since(started))
}

// compactAll compacts every open slot's spill.
func (r *ring) compactAll() {
	//: only slots with a spill moved.
	for s := r.lruHead; s != noLink; s = r.conns[s].lruNext {
		r.compactSpill(s)
	}
}

// now converts the wall clock into the loop's ticks.
func (lp *loop) now() int64 {
	//: monotonic, from the loop's start.
	return int64(time.Since(lp.epoch)/tickEvery) + 1
}

// lookup resolves ref to its ring and slot, or reports it stale.
func (lp *loop) lookup(ref corenet.LoopConnRef) (*ring, int32, bool) {
	//: a ring this loop does not have.
	if int(ref.Ring) >= len(lp.rings) {
		//: stale.
		return nil, 0, false
	}
	r := lp.rings[ref.Ring]
	//: a slot the table never reached.
	if int(ref.Slot) >= len(r.conns) {
		//: stale.
		return nil, 0, false
	}
	c := &r.conns[ref.Slot]
	//: another generation, or a connection already closing.
	if c.gen != ref.Gen || c.state != stOpen || ref.Gen == 0 {
		//: stale.
		return nil, 0, false
	}
	//: live.
	return r, int32(ref.Slot), true
}

// Write implements corenet.LoopIO: it copies p into ref's output.
func (lp *loop) Write(ref corenet.LoopConnRef, p []byte) error {
	//: the loop's thread only, checked under loopdebug.
	if lp.threadOK != nil && !lp.threadOK() {
		//: refused.
		return corenet.LoopWrongThread
	}
	r, slot, ok := lp.lookup(ref)
	//: a closed connection takes no more output.
	if !ok {
		//: stale.
		return corenet.LoopStaleRef
	}
	//: nothing to copy.
	if len(p) == 0 {
		//: done.
		return nil
	}
	c := &r.conns[slot]
	//: the per-connection ceiling counts what is still the kernel's too.
	if c.pending()+len(p) > lp.sz.outputCap {
		c.flags |= fWantWritable
		//: refused; OnWritable says when to retry.
		return corenet.LoopOutputFull
	}
	//: no chunk yet: attached at the first Write.
	if c.out.empty() {
		ch, ok := lp.slab.alloc(len(p))
		//: the loop's budget is spent.
		if !ok {
			c.flags |= fWantWritable
			//: refused.
			return corenet.LoopOutputFull
		}
		c.out = ch
	} else if lp.slab.size(c.out)-int(c.out.len) < len(p) {
		//: not enough room behind: moved to a chunk that holds it all.
		if !lp.growOut(c, len(p)) {
			c.flags |= fWantWritable
			//: refused.
			return corenet.LoopOutputFull
		}
	}
	c.out.len += int32(copy(lp.slab.bytes(c.out)[c.out.len:], p))
	r.markDirty(slot)
	//: queued, in order.
	return nil
}

// growOut moves c's unsent output to a chunk with room for n more bytes. The
// output is never what the kernel reads, so it may move; the flight never.
func (lp *loop) growOut(c *connCtl, n int) bool {
	held := int(c.out.len - c.out.off)
	ch, ok := lp.slab.alloc(held + n)
	//: refused by the class or the budget.
	if !ok {
		//: refused.
		return false
	}
	ch.len = int32(copy(lp.slab.bytes(ch), lp.slab.data(c.out)))
	lp.slab.free(c.out)
	c.out = ch
	//: moved.
	return true
}

// Close implements corenet.LoopIO: the connection closes once its output has
// left.
func (lp *loop) Close(ref corenet.LoopConnRef) error {
	//: the loop's thread only, checked under loopdebug.
	if lp.threadOK != nil && !lp.threadOK() {
		//: refused.
		return corenet.LoopWrongThread
	}
	r, slot, ok := lp.lookup(ref)
	//: already closing or closed.
	if !ok {
		//: stale.
		return corenet.LoopStaleRef
	}
	r.conns[slot].state = stClosing
	r.markDirty(slot)
	//: finished at the end of the batch.
	return nil
}

// SetDeadline implements corenet.LoopIO, on the loop's timer wheel.
func (lp *loop) SetDeadline(ref corenet.LoopConnRef, d time.Duration) error {
	//: the loop's thread only, checked under loopdebug.
	if lp.threadOK != nil && !lp.threadOK() {
		//: refused.
		return corenet.LoopWrongThread
	}
	r, slot, ok := lp.lookup(ref)
	//: a closed connection has no timer.
	if !ok {
		//: stale.
		return corenet.LoopStaleRef
	}
	c := &r.conns[slot]
	//: zero or less disarms.
	if d <= 0 {
		c.deadline = 0
	} else {
		//: rounded up, so it fires no earlier than asked.
		c.deadline = lp.now() + int64((d+tickEvery-1)/tickEvery)
	}
	r.armTimer(slot)
	//: armed.
	return nil
}

// Pressure implements corenet.LoopIO.
func (lp *loop) Pressure(ref corenet.LoopConnRef) bool {
	r, slot, ok := lp.lookup(ref)
	//: a stale reference is under no pressure.
	if !ok {
		//: none.
		return false
	}
	//: the accept's soft flag, or the loop's hard pressure since.
	return r.conns[slot].flags&fPressure != 0 || lp.hard
}

// Post implements corenet.LoopIO: the one call any goroutine may make.
func (lp *loop) Post(op corenet.PostFunc, arg uintptr) error {
	//: queued and the loop woken.
	return lp.post.post(op, arg)
}

// Loop implements corenet.LoopIO: the loop's index in its group, set at build
// and never written again, so any goroutine may read it.
func (lp *loop) Loop() int {
	//: fixed for the loop's life.
	return lp.index
}
