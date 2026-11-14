package http2

import (
	"sync"
	"sync/atomic"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

const (
	// slotHTTP2 marks a job's Slot as this adapter's own, apart from the embedded
	// HTTP/1.1 adapter's jobs: both offload through the lane's one worker.
	slotHTTP2 uint32 = 1 << 31
	// statusUnavailable answers a pending request its lane's full queue refused.
	statusUnavailable int = 503
)

// The package's vars: a 503's field and the drain's Post registry.
var (
	// retryAfter is the field a 503 for a full queue carries.
	retryAfter = []byte("Retry-After: 1\r\n")
	// drainKeys numbers the drain's Post arguments.
	drainKeys atomic.Uintptr
	// drainRegistry maps a drain's Post argument to its lane: an argument is
	// an integer and the registered op no closure, so the lane is found by a
	// key.
	drainRegistry sync.Map
)

// h2Worker is the corenet.LoopWorker the adapter offloads to: it runs the
// handler's HTTP1Worker on a slot of its own, or hands the job to the embedded
// HTTP/1.1 adapter's.
type h2Worker struct {
	// a is the adapter.
	a *LoopHandler
	// h1 is the embedded HTTP/1.1 adapter's worker.
	h1 corenet.LoopWorker
}

// Work implements corenet.LoopWorker, on the lane's worker goroutine.
func (w h2Worker) Work(lane int, job corenet.LoopJobValue) {
	//: the HTTP/1.1 adapter's own job.
	if job.Slot&slotHTTP2 == 0 {
		w.h1.Work(lane, job)
		//: run.
		return
	}
	slot := (*w.a.state(lane).slots.Load())[job.Slot&^slotHTTP2]
	//: a stream gone: no work for nobody.
	if slot.cancelled.Load() {
		//: skipped; OnDone frees the slot.
		return
	}
	w.a.worker.WorkHTTP1(lane, job.Arg, &slot.res)
}

// Worker implements corenet.LoopOffloader: the adapter offloads when its
// handler is a corenet.HTTP1Worker, and nothing otherwise.
func (a *LoopHandler) Worker() corenet.LoopWorker {
	//: a handler that answers nothing off the loop.
	if a.worker == nil {
		//: no worker.
		return nil
	}
	//: the handler's, for both versions.
	return h2Worker{a: a, h1: a.h1.Worker()}
}

// OnOutbound implements corenet.LoopOffloader: each lane keeps its outbound,
// the HTTP/1.1 adapter its own view of it.
func (a *LoopHandler) OnOutbound(v corenet.LoopOutboundValue) {
	a.holdCap = v.OutputCap
	a.h1.OnOutbound(v)
	//: each lane's own.
	for i, out := range v.Lanes {
		a.state(i).outbound = out
	}
}

// OnDone implements corenet.LoopOffloader, on the lane's thread: a job has
// come back. An HTTP/2 answer is written at once, the streams being
// independent; one whose stream or connection ended is dropped and its slot
// freed, the reset path.
func (a *LoopHandler) OnDone(io corenet.LoopIO, job corenet.LoopJobValue) {
	//: the HTTP/1.1 adapter's job.
	if job.Slot&slotHTTP2 == 0 {
		a.h1.OnDone(io, job)
		//: answered in its own order.
		return
	}
	st := a.state(io.Loop())
	idx := job.Slot &^ slotHTTP2
	slot := (*st.slots.Load())[idx]
	//: an older generation's job: the slot is another answer's now.
	if slot.gen != job.Gen {
		//: left to its holder.
		return
	}
	e := st.entry(slot.ref)
	//: the connection is gone, or the stream reset before the job returned.
	if slot.cancelled.Load() || !e.live() || e.gen != slot.ref.Gen {
		st.freeSlot(idx)
		//: dropped; nothing is an error here.
		return
	}
	a.answerSlot(io, st, e, slot, idx)
}

// answerSlot writes a worker's answer on its stream, ends the connection when
// it was the last of a closing one, and frees the slot.
func (a *LoopHandler) answerSlot(io corenet.LoopIO, st *laneState, e *connEntry, slot *workSlot, idx uint32) {
	c := h2Call{a: a, io: io, ref: slot.ref, st: st, e: e}
	off := len(st.out)
	c.answer(slot.stream, &slot.res, slot.isHead)
	//: the h2 reading of Connection: close.
	if slot.res.Close || io.Pressure(slot.ref) {
		c.goAway()
	}
	st.finishSlot(e, slot.stream)
	st.out = st.lane.AppendCredits(&e.conn, st.out)
	out := st.out[off:]
	//: written from the lane's thread, the connection's order kept by the loop.
	a.writeClose(io, slot.ref, out, st.lane.Closing(&e.conn) || st.lane.Done(&e.conn))
	st.out = st.out[:off]
	st.freeSlot(idx)
}

// finishSlot drops stream's record, its answer having left.
func (st *laneState) finishSlot(e *connEntry, stream uint32) {
	idx, prev := st.findRequest(e, stream)
	//: already dropped by a reset, or never needed.
	if idx == noIndex {
		//: nothing to finish.
		return
	}
	st.requests[idx].slot = noIndex
	st.finishRequest(e, idx, prev)
	//: outside a batch the release that the batch's end does happens here.
	if len(st.active) == 0 {
		st.releaseCompleted()
	}
}

// releaseCompleted returns the answered requests to the pool.
func (st *laneState) releaseCompleted() {
	//: each one.
	for _, idx := range st.completed {
		st.release(idx)
	}
	st.completed = st.completed[:0]
}

// offload queues the pending answer res on its worker, or answers it at once
// when no worker can take it. It returns the record that stays for the job.
func (c *h2Call) offload(stream uint32, res *corenet.HTTP1ResponseValue, isHead bool, idx int32) int32 {
	st := c.st
	//: Pending with Defer, or with no worker: a 500 on the stream.
	if res.Defer || c.a.worker == nil || st.outbound == nil {
		c.answerStatus(stream, statusInternal)
		//: no record kept.
		return idx
	}
	slot, slotIdx := st.takeSlot()
	slot.ref, slot.stream, slot.isHead = c.ref, stream, isHead
	slot.res = corenet.HTTP1ResponseValue{}
	slot.cancelled.Store(false)
	job := corenet.LoopJobValue{Ref: c.ref, Slot: slotIdx | slotHTTP2, Gen: slot.gen, Arg: res.Job}
	//: the worker's queue is full: 503, the connection kept.
	if err := st.outbound.Send(c.io, job); err != nil {
		st.freeSlot(slotIdx)
		c.answerUnavailable(stream)
		//: no record kept.
		return idx
	}
	//: a record ties the stream to its slot, for a reset before the job returns.
	if idx == noIndex {
		idx, _ = st.takeRequest(c.e, stream)
	}
	st.requests[idx].slot = int32(slotIdx)
	//: the record, which the job's return finishes.
	return idx
}

// answerUnavailable writes a 503 with Retry-After on stream.
func (c *h2Call) answerUnavailable(stream uint32) {
	st := c.st
	res := &corenet.HTTP1ResponseValue{Status: statusUnavailable, Header: retryAfter}
	out, err := st.lane.Headers(&c.e.conn, stream, res, true, st.out)
	//: every field of it is the adapter's own, so the writer takes it.
	if err == nil {
		st.out = out
	}
}

// takeSlot returns a free worker slot and its index, growing the table by
// publication, never in place, so the worker can read it without a lock.
func (st *laneState) takeSlot() (*workSlot, uint32) {
	//: the free list first.
	if n := len(st.freeSlots); n > 0 {
		idx := st.freeSlots[n-1]
		st.freeSlots = st.freeSlots[:n-1]
		slot := (*st.slots.Load())[idx]
		slot.gen++
		//: reused.
		return slot, idx
	}
	cur := *st.slots.Load()
	next := make([]*workSlot, len(cur)+1, max(2*len(cur), len(cur)+1))
	copy(next, cur)
	slot := &workSlot{idx: uint32(len(cur))}
	next[len(cur)] = slot
	st.slots.Store(&next)
	//: new.
	return slot, slot.idx
}

// freeSlot returns slot idx to the free list.
func (st *laneState) freeSlot(idx uint32) {
	st.freeSlots = append(st.freeSlots, idx)
}

// Draining implements corenet.LoopHTTPCodec, on the goroutine that drains
// the group: each lane is told through its own thread, and writes
// GOAWAY(NO_ERROR) on every HTTP/2 connection it holds.
func (a *LoopHandler) Draining() {
	//: once.
	if a.draining.Swap(true) {
		//: already announced.
		return
	}
	p := a.lanes.Load()
	//: no lane was ever built: nothing to tell.
	if p == nil {
		//: done.
		return
	}
	//: each lane through its own thread.
	for _, st := range *p {
		a.announce(st)
	}
}

// announce posts the drain's GOAWAY to st's thread; a lane with no connection
// yet, or whose queue refuses, skips it, and the drain closes what it finds.
func (*LoopHandler) announce(st *laneState) {
	holder := st.post.Load()
	//: no connection ever opened on this lane.
	if holder == nil {
		st.goAwayDone.Store(true)
		//: nothing to announce.
		return
	}
	key := drainKeys.Add(1)
	drainRegistry.Store(key, st)
	//: the queue refuses: the drain closes what it finds, without a GOAWAY.
	if err := holder.io.Post(runDrainGoAway, key); err != nil {
		drainRegistry.Delete(key)
		st.goAwayDone.Store(true)
	}
}

// runDrainGoAway is the op the drain posts: on the lane's thread, it writes
// GOAWAY on every HTTP/2 connection and closes those with no open stream.
func runDrainGoAway(io corenet.LoopIO, key uintptr) {
	v, ok := drainRegistry.LoadAndDelete(key)
	//: the registry is the op's only state.
	if !ok {
		//: already run.
		return
	}
	st, _ := v.(*laneState)
	st.announceDrain(io)
}

// announceDrain walks the lane's connections.
func (st *laneState) announceDrain(io corenet.LoopIO) {
	//: every ring and slot.
	for ring := range st.entries {
		//: every live HTTP/2 connection of the ring.
		for slot := range st.entries[ring] {
			e := &st.entries[ring][slot]
			//: a connection of another version or a free slot.
			if !e.live() || e.kind != kindHTTP2 {
				continue
			}
			st.announceConn(io, corenet.LoopConnRef{Ring: uint32(ring), Slot: uint32(slot), Gen: e.gen}, e)
		}
	}
	st.goAwayDone.Store(true)
}

// announceConn writes GOAWAY(NO_ERROR) on one connection and closes it when
// nothing is in flight. A connection the batch fed is left to the batch's
// end: the answers it was given wait in the batch's output, written at
// OnBatchEnd, which then closes it (closeIfDone); a close here would refuse
// them, and its streams, answered already, no longer count as in flight
// (#1385).
func (st *laneState) announceConn(io corenet.LoopIO, ref corenet.LoopConnRef, e *connEntry) {
	//: one GOAWAY a connection, whoever asked first.
	if e.flags&entryGoAway == 0 {
		e.flags |= entryGoAway
		st.jobOut = st.lane.GoAway(&e.conn, corehttp2.ErrCodeNoError, st.jobOut[:0])
		swallowErr(io.Write(ref, st.jobOut))
	}
	//: nothing in flight and nothing the batch still owes it: it ends now.
	if st.lane.Done(&e.conn) && e.activeAt != st.lane.epoch {
		swallowErr(io.Close(ref))
	}
}

// InFlight implements corenet.LoopHTTPCodec, on the lane's thread: ref, a
// connection of lane, must stay open while the drain's GOAWAY has not gone
// out, or its streams are still being answered.
func (a *LoopHandler) InFlight(lane int, ref corenet.LoopConnRef) bool {
	st := a.state(lane)
	e := st.peek(ref)
	//: not a connection this adapter holds, or not HTTP/2.
	if e == nil || e.kind != kindHTTP2 {
		//: settled, as far as this adapter knows.
		return false
	}
	//: the drain's GOAWAY is still on its way to this lane.
	if a.draining.Load() && !st.goAwayDone.Load() {
		//: kept.
		return true
	}
	//: open streams are being answered.
	return st.lane.OpenStreams(&e.conn) > 0
}

// Pending implements corenet.LoopOffloader: a connection waiting for answers
// on either version is still being served.
func (a *LoopHandler) Pending(lane int, ref corenet.LoopConnRef) bool {
	//: HTTP/1.1's held answers, or an HTTP/2 connection in flight.
	return a.h1.Pending(lane, ref) || a.InFlight(lane, ref)
}

// peek returns ref's entry when it is ref's connection's, nil otherwise; it
// resets nothing.
func (st *laneState) peek(ref corenet.LoopConnRef) *connEntry {
	ring, slot := int(ref.Ring), int(ref.Slot)
	//: never seen.
	if ring >= len(st.entries) || slot >= len(st.entries[ring]) {
		//: none.
		return nil
	}
	e := &st.entries[ring][slot]
	//: another connection's.
	if !e.live() || e.gen != ref.Gen {
		//: none.
		return nil
	}
	//: the connection's own.
	return e
}
