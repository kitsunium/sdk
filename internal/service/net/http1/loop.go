package http1

import (
	"bytes"
	"sync"
	"sync/atomic"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// internalError is the whole answer to a response the writer refused or a
// handler left deferred: a 500 with no body, and the connection closed.
const internalError string = "HTTP/1.1 500 Internal Server Error\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"

// HandleLoop adapts h to the loop engine: the corenet.LoopHandler a group
// serves with StreamGroup.HandleLoop. p frames every request, so its limits
// are the group's; the zero Parser is the profile's defaults.
//
// Each loop frames its requests into a request value of its own, sets its
// Loop, and calls h on the loop's thread, one request at a time, in the order
// they arrived, pipelined ones included. A refused request is answered and
// its connection closed without reaching h. An answer has AppendResponse's
// framing, with Connection: close when the request, the response or the
// loop's soft pressure (LoopIO.Pressure) asks for it, and nothing more is
// read from a connection that closes. The batch's answers are written at
// its end, after h's EndBatch when h is a corenet.HTTP1Batcher, in request
// order on each connection, deferred ones in their place.
//
// The response value h fills starts every request with a zero status and a
// nil Header and Body: h points them at bytes it owns, valid until the
// answer is taken — when ServeHTTP1 returns, or when EndBatch returns for a
// deferred one. An answer AppendResponse refuses is answered 500 and its
// connection closed, and so is a deferred answer EndBatch left deferred. An
// answer the connection's output refuses (LoopOutputFull: OutputCap is the
// most a connection's answers to one batch may hold) closes the connection
// rather than wait. A request the spill cannot hold whole (LoopValue.SpillCap
// below p's head and body bounds) closes it too, with LoopSpillFull.
func HandleLoop(h corenet.HTTP1Handler, p Parser) *LoopHandler {
	b, _ := h.(corenet.HTTP1Batcher)
	//: the batcher is found once, never per batch.
	return &LoopHandler{h: h, batcher: b, p: p}
}

// LoopHandler serves the restricted HTTP/1.1 profile on a loop group: the
// corenet.LoopHandler HandleLoop returns. Its state is per loop — a request
// value, the batch's answers, a flag per connection — and a loop touches only
// its own, found by LoopIO.Loop, so the data path takes no lock and, once a
// loop's buffers have grown to its batches, allocates nothing. It honours no
// group option itself: a loop group given ReadHeaderTimeout or MaxHeaderBytes
// refuses it at Start, the head bound being p's.
type LoopHandler struct {
	// h serves every request.
	h corenet.HTTP1Handler
	// batcher is h as an HTTP1Batcher, nil when it is not one.
	batcher corenet.HTTP1Batcher
	// p frames every request.
	p Parser
	// loops holds each loop's state at its LoopIO.Loop index. It is replaced,
	// never written in place, under mu; a loop reads it without the lock.
	loops atomic.Pointer[[]*loopState]
	// mu serialises the first sight of a loop.
	mu sync.Mutex
}

// loopState is one loop's state. Only that loop's thread touches it.
type loopState struct {
	// req is the request value every request of the loop is framed into.
	req corenet.HTTP1RequestValue
	// stub carries the Method and Close a deferred answer is written with,
	// once req has moved on to the next request.
	stub corenet.HTTP1RequestValue
	// out holds the batch's answers, written in place as each is taken.
	out []byte
	// spare is the buffer complete orders a batch with deferred answers into;
	// it and out swap, so neither is allocated again.
	spare []byte
	// deferred counts the batch's deferred answers.
	deferred int
	// items are the batch's answers, in the order their requests arrived.
	items []item
	// free are the response values no request holds.
	free []*corenet.HTTP1ResponseValue
	// conns is a flag per connection, by ring and slot.
	conns [][]connFlag
	// served counts the requests the batch handed to the handler.
	served int
}

// item is one answer of a batch.
type item struct {
	// ref is the connection it answers on.
	ref corenet.LoopConnRef
	// res is the deferred response, written at the batch's end; nil once the
	// answer is in out.
	res *corenet.HTTP1ResponseValue
	// off and end bound the answer in out.
	off, end int
	// head is whether the request was a HEAD, for a deferred answer.
	head bool
	// closing is whether the connection closes after this answer.
	closing bool
}

// connFlag is what the adapter keeps of one connection: its generation, so a
// stale slot reads as a new one, and how it ends.
type connFlag struct {
	// gen is the generation of the connection the flag describes.
	gen uint32
	// closing is set once an answer that closes the connection is queued:
	// nothing more is read from it.
	closing bool
	// dead is set once a write was refused or the close was asked: nothing
	// more is written to it.
	dead bool
}

// OnOpen implements corenet.LoopHandler. It marks the connection open on its loop.
func (a *LoopHandler) OnOpen(io corenet.LoopIO, ref corenet.LoopConnRef) {
	*a.state(io.Loop()).flag(ref) = connFlag{gen: ref.Gen}
}

// OnData implements corenet.LoopHandler. It frames every whole request at the
// start of buf, serves each in turn, and returns the bytes they took: a
// request still incomplete waits in the loop's spill for its next bytes. A
// refused request, or one whose answer closes the connection, takes the rest
// of buf, which is never read.
func (a *LoopHandler) OnData(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	loop := io.Loop()
	st := a.state(loop)
	f := st.flag(ref)
	//: a connection whose last answer closes it reads nothing more.
	if f.gen == ref.Gen && f.closing {
		//: everything is dropped with the connection.
		return len(buf)
	}
	used := 0
	//: every whole request at the start of what is left.
	for used < len(buf) {
		n, r := a.p.Parse(buf[used:], &st.req)
		//: refused: answered by the codec, and the connection closed.
		if r != RefusalNone {
			off := len(st.out)
			st.out = AppendRefusal(st.out, r)
			st.items = append(st.items, item{ref: ref, off: off, end: len(st.out), closing: true})
			f.closing = true
			//: the rest is never read.
			return len(buf)
		}
		//: a request still incomplete waits for its next bytes.
		if n == 0 {
			break
		}
		used += n
		//: the answer closes the connection: nothing past it is read.
		if a.serve(io, ref, st, loop) {
			f.closing = true
			//: the rest is never read.
			return len(buf)
		}
	}
	//: the whole requests; a partial one stays in the spill.
	return used
}

// serve hands the request framed into st.req to the handler and queues its
// answer, and reports whether the connection closes after it.
func (a *LoopHandler) serve(io corenet.LoopIO, ref corenet.LoopConnRef, st *loopState, loop int) bool {
	st.req.Loop = loop
	res := st.take()
	st.served++
	a.h.ServeHTTP1(&st.req, res)
	closing := st.req.Close || res.Close || io.Pressure(ref)
	//: deferred: the response value is kept, and written at the batch's end.
	if res.Defer {
		st.deferred++
		st.items = append(st.items, item{
			ref: ref, res: res, head: bytes.Equal(st.req.Method, methodHead), closing: closing,
		})
		//: whether the connection closes is known now.
		return closing
	}
	off := len(st.out)
	out, err := AppendResponse(st.out, &st.req, res, closing)
	//: a response the writer refuses is answered 500, and the connection closed.
	if err != nil {
		out = append(st.out, internalError...)
		closing = true
	}
	st.out = out
	st.items = append(st.items, item{ref: ref, off: off, end: len(st.out), closing: closing})
	st.give(res)
	//: as the request, the response or the pressure decided.
	return closing
}

// OnTimeout implements corenet.LoopHandler. The adapter arms no deadline, so a
// timeout it did not ask for closes the connection.
func (*LoopHandler) OnTimeout(io corenet.LoopIO, ref corenet.LoopConnRef) {
	//: a stale reference is the only refusal, and means already closed.
	swallowErr(io.Close(ref))
}

// OnWritable implements corenet.LoopHandler. Nothing waits on it: an answer
// the output refuses closes its connection.
func (*LoopHandler) OnWritable(io corenet.LoopIO, ref corenet.LoopConnRef) {
	//: nothing waits on it; the arguments are the port's.
	_, _ = io, ref
}

// OnBatchEnd implements corenet.LoopHandler. When the batch served a request,
// it calls the handler's EndBatch if it has one, then writes the batch's
// answers in order and closes the connections whose answer asked to.
func (a *LoopHandler) OnBatchEnd(io corenet.LoopIO) {
	loop := io.Loop()
	st := a.state(loop)
	//: a batch with no answer has nothing to end.
	if len(st.items) == 0 {
		//: nothing queued.
		return
	}
	//: the handler's batched work completes its deferred answers.
	if st.served > 0 && a.batcher != nil {
		a.batcher.EndBatch(loop)
	}
	st.complete()
	st.flush(io)
	st.items, st.out, st.served = st.items[:0], st.out[:0], 0
}

// OnClose implements corenet.LoopHandler. The connection's flag stays until
// the next OnOpen of its slot replaces it; an answer of the batch still
// queued for it is dropped when its Write is refused as stale.
func (*LoopHandler) OnClose(io corenet.LoopIO, ref corenet.LoopConnRef, err error) {
	//: the flag is replaced by the slot's next OnOpen; the arguments are the port's.
	_, _, _ = io, ref, err
}

// complete writes the batch's answers again into spare, in request order,
// each deferred one in its place, and makes spare the batch's out: the
// answers of one connection then lie side by side, and leave in one Write.
// A batch with no deferred answer is already in order and is left as it is.
func (st *loopState) complete() {
	//: nothing deferred: out is already in request order.
	if st.deferred == 0 {
		//: as taken.
		return
	}
	out := st.spare[:0]
	//: in arrival order, so each answer lands in its place.
	for i := range st.items {
		it := &st.items[i]
		off := len(out)
		//: written when it was taken: moved, not written again.
		if it.res == nil {
			out = append(out, st.out[it.off:it.end]...)
			it.off, it.end = off, len(out)
			continue
		}
		res := it.res
		st.stub.Method, st.stub.Close = nil, false
		//: the one request field the writer reads besides Close.
		if it.head {
			st.stub.Method = methodHead
		}
		next, err := AppendResponse(out, &st.stub, res, it.closing)
		//: left deferred, or refused by the writer: 500, and closed.
		if res.Defer || err != nil {
			next = append(out, internalError...)
			it.closing = true
		}
		it.closing = it.closing || res.Close
		out, it.off, it.end, it.res = next, off, len(next), nil
		st.give(res)
	}
	st.spare, st.out, st.deferred = st.out, out, 0
}

// flush writes the batch's answers in order, a run of one connection's
// answers that lie side by side in out in one Write, and closes each
// connection whose answer asked to, or whose output refused one.
func (st *loopState) flush(io corenet.LoopIO) {
	//: one run at a time.
	for i := 0; i < len(st.items); {
		it := st.items[i]
		end, closing := it.end, it.closing
		j := i + 1
		//: the next answers on the same connection, contiguous in out.
		for ; j < len(st.items) && !closing && st.items[j].ref == it.ref && st.items[j].off == end; j++ {
			end, closing = st.items[j].end, st.items[j].closing
		}
		st.write(io, it.ref, st.out[it.off:end], closing)
		i = j
	}
}

// write writes p on ref, then closes ref when closing or when the write is
// refused. A connection already given up on is skipped.
func (st *loopState) write(io corenet.LoopIO, ref corenet.LoopConnRef, p []byte, closing bool) {
	f := st.flag(ref)
	//: closed, or refused once: nothing more is written to it.
	if f.gen == ref.Gen && f.dead {
		//: dropped.
		return
	}
	//: written in full, and kept open unless asked otherwise.
	if err := io.Write(ref, p); err == nil && !closing {
		//: written.
		return
	}
	f.dead = true
	//: a stale reference is the only refusal, and means already closed.
	swallowErr(io.Close(ref))
}

// take returns a zeroed response value for one request.
func (st *loopState) take() *corenet.HTTP1ResponseValue {
	//: none free: the pool grows by one, once.
	if len(st.free) == 0 {
		//: a fresh value is already zero.
		return &corenet.HTTP1ResponseValue{}
	}
	res := st.free[len(st.free)-1]
	st.free = st.free[:len(st.free)-1]
	*res = corenet.HTTP1ResponseValue{}
	//: the pooled value, cleared.
	return res
}

// give returns res to the pool. Its slices are dropped at the next take,
// never reused: they are the handler's.
func (st *loopState) give(res *corenet.HTTP1ResponseValue) {
	st.free = append(st.free, res)
}

// flag returns ref's flag, growing the table on the first sight of its ring
// or its slot. A flag of an older generation reads as nothing set.
func (st *loopState) flag(ref corenet.LoopConnRef) *connFlag {
	ring, slot := int(ref.Ring), int(ref.Slot)
	//: a ring seen for the first time.
	if ring >= len(st.conns) {
		st.conns = append(st.conns, make([][]connFlag, ring+1-len(st.conns))...)
	}
	//: a slot past the table: grown by doubling, so a ring's growth is paid a
	//: few times in its life, never per connection.
	if slot >= len(st.conns[ring]) {
		grown := make([]connFlag, max(slot+1, 2*len(st.conns[ring])))
		copy(grown, st.conns[ring])
		st.conns[ring] = grown
	}
	f := &st.conns[ring][slot]
	//: a slot reused by a newer connection starts clean.
	if f.gen != ref.Gen {
		*f = connFlag{gen: ref.Gen}
	}
	//: the connection's own flag.
	return f
}

// state returns loop i's state, built on the loop's first call.
func (a *LoopHandler) state(i int) *loopState {
	//: the common case: the loop has been seen.
	if p := a.loops.Load(); p != nil && i < len(*p) && (*p)[i] != nil {
		//: no lock taken.
		return (*p)[i]
	}
	//: once per loop.
	return a.grow(i)
}

// grow builds loop i's state and publishes a table that holds it.
func (a *LoopHandler) grow(i int) *loopState {
	a.mu.Lock()
	defer a.mu.Unlock()
	var cur []*loopState
	//: the table as the other loops left it.
	if p := a.loops.Load(); p != nil {
		cur = *p
	}
	//: built meanwhile by nobody else, since only loop i asks for i; checked
	//: all the same.
	if i < len(cur) && cur[i] != nil {
		//: already there.
		return cur[i]
	}
	next := make([]*loopState, max(len(cur), i+1))
	copy(next, cur)
	st := &loopState{}
	st.req.Loop = i
	next[i] = st
	a.loops.Store(&next)
	//: published.
	return st
}

// swallowErr intentionally discards a non-actionable error, recording the
// discard so the error audit treats it as deliberate: the adapter has no
// caller to return a refused Close to, and a refused Close means the
// connection is already gone.
func swallowErr(err error) {
	//: read the parameter so the unused-error audit treats this as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
	//: the connection is already closing or closed.
}
