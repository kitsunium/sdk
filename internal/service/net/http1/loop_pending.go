package http1

import (
	"bytes"
	"sync/atomic"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// statusUnavailable answers a pending request its lane's full queue to the
	// worker refused.
	statusUnavailable int = 503
	// defaultHoldCap bounds what one connection holds back behind a pending
	// answer when the group gave no OutputCap: the loop's own default.
	defaultHoldCap int = 1 << 20
)

// retryAfter is the field a 503 for a full queue carries: one second, the
// least a client can be told.
var retryAfter = []byte("Retry-After: 1\r\n")

// pendSlot is one pending answer (R3): what the lane keeps of it while the
// worker fills res. The lane's thread writes ref, head and closing before the
// job is sent and reads res once it is back; the worker writes res only, and
// reads cancelled. Slots are never moved, so the worker holds one by pointer.
type pendSlot struct {
	// idx is the slot's index in the table, fixed.
	idx uint32
	// gen is the slot's generation, moved on at each take.
	gen uint32
	// ref is the connection it answers.
	ref corenet.LoopConnRef
	// res is the answer the worker fills.
	res corenet.HTTP1ResponseValue
	// stub carries the request's Method — HEAD or none — and whether the
	// connection closes after the answer (Close), as the writer reads them.
	stub corenet.HTTP1RequestValue
	// done is set on the lane's thread once the job is back.
	done bool
	// cancelled is set once the connection closed: the worker skips the work,
	// and the answer is dropped.
	cancelled atomic.Bool
}

// heldItem is one answer a connection holds back behind a pending one, in
// request order: a pending slot, a deferred response the batch's end
// completes, or bytes already written into the connection's buffer.
type heldItem struct {
	// slot is the pending answer; nil otherwise.
	slot *pendSlot
	// res is a deferred response not yet completed; nil otherwise.
	res *corenet.HTTP1ResponseValue
	// off and end bound the answer's bytes in the connection's buffer.
	off, end int
	// stub carries a deferred request's Method — HEAD or none — for the
	// writer, and whether the connection closes after this answer (Close).
	stub *corenet.HTTP1RequestValue
	// closing is whether the connection closes after this answer.
	closing bool
}

// pendingWorker is the corenet.LoopWorker the adapter offloads to: it runs
// the handler's HTTP1Worker on the slot's response.
type pendingWorker struct {
	// a is the adapter.
	a *LoopHandler
}

// Work implements corenet.LoopWorker, on the lane's worker goroutine: it
// fills the slot's response unless its connection has closed.
func (w pendingWorker) Work(lane int, job corenet.LoopJobValue) {
	slot := (*w.a.state(lane).slots.Load())[job.Slot]
	//: a connection gone: no work for nobody.
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
	//: the handler's.
	return pendingWorker{a: a}
}

// OnOutbound implements corenet.LoopOffloader: each lane keeps its outbound,
// and every connection's holdback is bounded by the output ceiling.
func (a *LoopHandler) OnOutbound(v corenet.LoopOutboundValue) {
	a.holdCap = orDefault(v.OutputCap, defaultHoldCap)
	//: each lane's own.
	for i, out := range v.Lanes {
		a.state(i).outbound = out
	}
}

// OnDone implements corenet.LoopOffloader, on the lane's thread: the job's
// answer is ready, and its connection is flushed at the batch's end, in
// request order. A connection that closed meanwhile has its slot freed and
// nothing written.
func (a *LoopHandler) OnDone(io corenet.LoopIO, job corenet.LoopJobValue) {
	st := a.state(io.Loop())
	slot := (*st.slots.Load())[job.Slot]
	f := st.peek(slot.ref)
	//: the connection is gone, or the answer was given up on.
	if slot.gen != job.Gen || slot.cancelled.Load() || f == nil {
		st.freeSlot(job.Slot)
		//: dropped.
		return
	}
	slot.done = true
	st.markReady(slot.ref, f)
}

// Pending implements corenet.LoopOffloader: a connection holding answers
// behind a pending one is still being answered, so a draining loop keeps it.
func (a *LoopHandler) Pending(lane int, ref corenet.LoopConnRef) bool {
	f := a.state(lane).peek(ref)
	//: answers still held.
	return f != nil && len(f.held) > 0
}

// answer is one answer hold queues: the connection, its flag, the response
// and whether the connection closes after it.
type answer struct {
	// io is the lane's LoopIO.
	io corenet.LoopIO
	// ref is the connection.
	ref corenet.LoopConnRef
	// f is its flag.
	f *connFlag
	// res is the handler's response.
	res *corenet.HTTP1ResponseValue
	// closing is whether the connection closes after it.
	closing bool
}

// hold queues an answer behind the connection's pending one, or as one when
// it is pending, and reports whether the connection closes after it. It
// holds every answer of a connection from its first pending one until the
// batch's end that flushes them; a connection whose held answers pass the
// output ceiling is closed.
func (a *LoopHandler) hold(st *loopState, an *answer) bool {
	f, res := an.f, an.res
	//: by what the handler asked.
	switch {
	//: handed to the worker, or refused.
	case res.Pending:
		a.holdPending(st, an)
	//: deferred behind a pending one: completed at the batch's end.
	case res.Defer:
		f.held = append(f.held, heldItem{res: res, stub: st.stubOf(isHead(st.req.Method)), closing: an.closing})
		st.deferHeld = append(st.deferHeld, an.ref)
	//: answered now, held in order.
	default:
		st.holdBytes(an, res)
		st.give(res)
	}
	st.markReady(an.ref, f)
	//: past the ceiling: the connection goes, its held answers with it.
	if len(f.buf) > a.holdCap {
		st.dropHeld(f)
		f.dead = true
		swallowErr(an.io.Close(an.ref))
		//: nothing more is read.
		return true
	}
	//: as the request, the response or the pressure decided.
	return an.closing
}

// holdPending sends a pending answer's job to the lane's worker and holds its
// place; a full queue holds a 503 in its place, and Pending with Defer, or
// with no worker, a 500 that closes the connection.
func (a *LoopHandler) holdPending(st *loopState, an *answer) {
	res := an.res
	st.give(res)
	//: a misuse: nothing can run it.
	if res.Defer || a.worker == nil || st.outbound == nil {
		an.closing = true
		an.f.buf = append(an.f.buf, internalError...)
		an.f.held = append(an.f.held, heldItem{off: len(an.f.buf) - len(internalError), end: len(an.f.buf), closing: true})
		//: answered 500.
		return
	}
	idx, slot := st.takeSlot()
	slot.ref = an.ref
	slot.stub.Method, slot.stub.Close = nil, an.closing
	//: the one request field the writer reads besides Close.
	if isHead(st.req.Method) {
		slot.stub.Method = methodHead
	}
	//: queued: the answer waits for the worker.
	if st.outbound.Send(an.io, corenet.LoopJobValue{Ref: an.ref, Slot: idx, Gen: slot.gen, Arg: res.Job}) == nil {
		an.f.held = append(an.f.held, heldItem{slot: slot, closing: an.closing})
		//: sent.
		return
	}
	st.freeSlot(idx)
	st.holdBytes(an, &st.unavailable)
}

// holdBytes writes res into the connection's buffer and holds it in order; a
// response the writer refuses is a 500 that closes the connection.
func (st *loopState) holdBytes(an *answer, res *corenet.HTTP1ResponseValue) {
	f := an.f
	off := len(f.buf)
	out, err := AppendResponse(f.buf, &st.req, res, an.closing)
	//: refused by the writer: 500, and closed.
	if err != nil {
		out = append(f.buf, internalError...)
		an.closing = true
	}
	f.buf = out
	f.held = append(f.held, heldItem{off: off, end: len(f.buf), closing: an.closing})
}

// stubOf is the request the writer reads for a deferred answer: HEAD, or no
// method; one shared value of each, never written.
func (st *loopState) stubOf(head bool) *corenet.HTTP1RequestValue {
	//: a HEAD answer carries no body.
	if head {
		//: the HEAD stub.
		return &st.headStub
	}
	//: the plain one.
	return &st.plainStub
}

// closed gives up the held answers of ref when the flag is still its own.
func (a *LoopHandler) closed(io corenet.LoopIO, ref corenet.LoopConnRef) {
	st := a.state(io.Loop())
	//: the connection's own flag, never a newer one's.
	if f := st.peek(ref); f != nil {
		st.dropHeld(f)
	}
}

// isHead reports whether method is HEAD.
func isHead(method []byte) bool {
	//: the one method whose answer carries no body.
	return bytes.Equal(method, methodHead)
}

// takeSlot returns a free slot and its index, its generation moved on and
// its response zeroed; the table grows by doubling when none is free,
// published anew so the worker never reads a table being written.
func (st *loopState) takeSlot() (uint32, *pendSlot) {
	//: none free: grown.
	if len(st.freeSlots) == 0 {
		st.growSlots()
	}
	idx := st.freeSlots[len(st.freeSlots)-1]
	st.freeSlots = st.freeSlots[:len(st.freeSlots)-1]
	slot := (*st.slots.Load())[idx]
	slot.gen++
	slot.res, slot.done = corenet.HTTP1ResponseValue{}, false
	slot.cancelled.Store(false)
	//: taken.
	return idx, slot
}

// growSlots doubles the slot table, the new slots free.
func (st *loopState) growSlots() {
	var cur []*pendSlot
	//: the table so far.
	if p := st.slots.Load(); p != nil {
		cur = *p
	}
	next := make([]*pendSlot, len(cur), max(2*len(cur), 8))
	copy(next, cur)
	//: the new slots, all free.
	for i := len(cur); i < cap(next); i++ {
		next = append(next, &pendSlot{idx: uint32(i)})
		st.freeSlots = append(st.freeSlots, uint32(i))
	}
	st.slots.Store(&next)
}

// freeSlot returns slot idx to the free list.
func (st *loopState) freeSlot(idx uint32) {
	st.freeSlots = append(st.freeSlots, idx)
}

// markReady queues ref's connection for the batch's end, once a batch.
func (st *loopState) markReady(ref corenet.LoopConnRef, f *connFlag) {
	//: already queued this batch.
	if f.readyAt == st.readyEpoch {
		//: once.
		return
	}
	f.readyAt = st.readyEpoch
	st.ready = append(st.ready, ref)
}

// dropHeld gives up every answer f holds: a pending slot whose job is back is
// freed, one still out is cancelled and freed when it comes back, a deferred
// response is returned to the pool.
func (st *loopState) dropHeld(f *connFlag) {
	//: each held answer.
	for _, it := range f.held {
		//: by kind.
		switch {
		case it.slot != nil && it.slot.done:
			st.freeSlot(it.slot.idx)
		case it.slot != nil:
			it.slot.cancelled.Store(true)
		case it.res != nil:
			st.give(it.res)
		}
	}
	f.held, f.buf = f.held[:0], f.buf[:0]
}

// completeHeld completes, after EndBatch, the deferred responses held behind
// a pending answer: each becomes bytes in its connection's buffer.
func (st *loopState) completeHeld() {
	//: each connection that held one.
	for _, ref := range st.deferHeld {
		//: closed meanwhile: dropHeld already returned them.
		if f := st.peek(ref); f != nil {
			st.completeConn(ref, f)
		}
	}
	st.deferHeld = st.deferHeld[:0]
}

// completeConn writes f's deferred held responses into its buffer.
func (st *loopState) completeConn(ref corenet.LoopConnRef, f *connFlag) {
	//: in order.
	for i := range f.held {
		it := &f.held[i]
		//: a deferred one.
		if it.res != nil {
			off := len(f.buf)
			var closing bool
			f.buf, closing = appendHeld(f.buf, it.stub, it.res, it.closing)
			it.closing, it.off, it.end = closing, off, len(f.buf)
			st.give(it.res)
			it.res = nil
		}
	}
	st.markReady(ref, f)
}

// appendHeld appends res's answer and reports whether the connection closes
// after it: a response left deferred or pending, or one the writer refuses,
// is a 500 that closes it.
func appendHeld(dst []byte, stub *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue, closing bool) ([]byte, bool) {
	out, err := AppendResponse(dst, stub, res, closing)
	//: left deferred or pending, or refused by the writer: 500, and closed.
	if res.Defer || res.Pending || err != nil {
		//: closed.
		return append(dst, internalError...), true
	}
	//: as asked.
	return out, closing || res.Close
}

// flushReady writes, for each connection queued, the answers it holds from
// the first up to the first still out, in one Write, then closes it when one
// of them asked to.
func (st *loopState) flushReady(io corenet.LoopIO) {
	//: each connection once.
	for _, ref := range st.ready {
		//: closed meanwhile.
		if f := st.peek(ref); f != nil {
			st.flushHeld(io, ref, f)
		}
	}
	st.ready = st.ready[:0]
	st.readyEpoch++
}

// flushHeld writes f's ready answers, in order.
func (st *loopState) flushHeld(io corenet.LoopIO, ref corenet.LoopConnRef, f *connFlag) {
	out, n, closing := st.held[:0], 0, false
	//: from the first, while ready, up to one that closes.
	for ; n < len(f.held) && !closing && f.held[n].ready(); n++ {
		out, closing = st.appendItem(out, f, f.held[n])
	}
	st.held = out
	//: nothing ready.
	if n == 0 {
		//: waiting.
		return
	}
	rest := copy(f.held, f.held[n:])
	f.held = f.held[:rest]
	//: nothing held any more: the buffer starts over.
	if rest == 0 {
		f.buf = f.buf[:0]
	}
	st.write(io, ref, out, closing)
	//: closed: what it still holds goes with it.
	if closing || f.dead {
		st.dropHeld(f)
	}
}

// ready reports whether the held answer can be written: its bytes are in the
// buffer, or its job is back.
func (it heldItem) ready() bool {
	//: still out, or still deferred: it and the rest wait.
	return (it.slot == nil || it.slot.done) && it.res == nil
}

// appendItem appends one ready held answer to out and reports whether the
// connection closes after it; a pending one's slot is freed.
func (st *loopState) appendItem(out []byte, f *connFlag, it heldItem) ([]byte, bool) {
	//: bytes already written.
	if it.slot == nil {
		//: copied.
		return append(out, f.buf[it.off:it.end]...), it.closing
	}
	next, closing := appendHeld(out, &it.slot.stub, &it.slot.res, it.slot.stub.Close)
	st.freeSlot(it.slot.idx)
	//: the worker's answer.
	return next, closing
}
