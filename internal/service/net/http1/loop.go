package http1

import (
	"bytes"
	"sync"
	"sync/atomic"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// internalError is the whole answer to a response the writer refused or a
// handler left deferred: a 500 with no body, and the connection closed.
const internalError string = "HTTP/1.1 500 Internal Server Error\r\n" + internalErrorTail

// internalErrorTail is what follows the status line of that answer.
const internalErrorTail string = "Content-Length: 0\r\nConnection: close\r\n\r\n"

const (
	// _ is a connection reading on, the zero mode.
	_ connMode = iota
	// connClosing closes after the batch's answer: nothing more is read.
	connClosing
	// connStreaming is answering with a Stream: nothing more is read, and what is
	// written next is the handler's.
	connStreaming
	// connUpgraded is past a 101: every later byte is HTTP1Upgrader's.
	connUpgraded
)

// What one turn of OnData decided.
const (
	// stepMore reads on: more of the buffer can be used.
	stepMore stepKind = iota
	// stepWait stops: the rest waits in the spill for its next bytes.
	stepWait
	// stepClose stops: the connection closes and nothing more is read.
	stepClose
)

// HandleLoop adapts h to the loop engine: the corenet.LoopHandler a group
// serves with StreamGroup.HandleLoop. p frames every request, so its limits
// are the group's; the zero Parser is the profile's defaults.
//
// Each lane frames its requests into a request value of its own, sets its
// Lane, and calls h on the lane's thread, one request at a time, in the order
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
// rather than wait. A group's SpillCap and p's bounds agree at Start
// (CheckSpill): a default body bound is capped at what the spill holds beside
// the head, so a body past it is answered 413 on the head instead of closing
// with LoopSpillFull, and an explicit bound past it fails Start. A head past
// SpillCap closes with LoopSpillFull. When h is a corenet.HTTP1BodyReader the
// spill holds only heads: each chunk goes to OnBody as it arrives, and the
// request is served when the last has.
func HandleLoop(h corenet.HTTP1Handler, p Parser) *LoopHandler {
	//: everything else is found once, in newLoopHandler.
	return newLoopHandler(h, p)
}

// newLoopHandler finds h's optional capabilities once, never per batch.
func newLoopHandler(h corenet.HTTP1Handler, p Parser) *LoopHandler {
	b, _ := h.(corenet.HTTP1Batcher)
	w := findWorker(h)
	r, _ := h.(corenet.HTTP1BodyReader)
	ab, _ := h.(corenet.HTTP1BodyAborter)
	//: the batcher, the worker, the body reader and the stream port.
	streamer := findStreamer(h)
	up, _ := streamer.(corenet.HTTP1Upgrader)
	return &LoopHandler{h: h, batcher: b, worker: w, reader: r, aborter: ab, streamer: streamer, upgrader: up, canceller: findCanceller(h), p: p, holdCap: defaultHoldCap}
}

// findStreamer walks a chain of links down to the handler that speaks the
// stream port, so a stream may run behind Routes, Bearer or Log. The chain was
// built once, here it is read once.
func findStreamer(h corenet.HTTP1Handler) corenet.HTTP1Streamer {
	//: down the chain, every link unwrapped.
	for {
		//: the first handler that streams.
		if s, ok := h.(corenet.HTTP1Streamer); ok {
			//: found.
			return s
		}
		l, ok := h.(interface{ wrapped() corenet.HTTP1Handler })
		//: a handler that wraps nothing: no streamer.
		if !ok {
			//: none.
			return nil
		}
		h = l.wrapped()
	}
}

// findWorker walks a chain of links down to the handler that takes a pending
// answer's job, so a Files link or a worker sits behind Routes, Bearer or Log.
func findWorker(h corenet.HTTP1Handler) corenet.HTTP1Worker {
	//: down the chain, every link unwrapped.
	for {
		//: the first handler that works.
		if w, ok := h.(corenet.HTTP1Worker); ok {
			//: found.
			return w
		}
		l, ok := h.(interface{ wrapped() corenet.HTTP1Handler })
		//: a handler that wraps nothing: no worker.
		if !ok {
			//: none.
			return nil
		}
		h = l.wrapped()
	}
}

// strayFile answers a file set outside a worker's answer: a 500, its reader
// closed, the connection closing. Only a worker's answer may hold one.
func strayFile(res *corenet.HTTP1ResponseValue, closing bool) bool {
	//: as the handler left it.
	if res.File.R == nil || res.Pending {
		//: nothing to refuse.
		return closing
	}
	closeFileReader(res.File.R)
	res.File = corenet.HTTP1FileValue{}
	res.Status, res.Body, res.Header = statusInternal, nil, nil
	res.Close = true
	//: refused.
	return true
}

// holds reports whether an answer of the connection must wait in order behind
// a pending answer or a file still leaving.
func (f *connFlag) holds(res *corenet.HTTP1ResponseValue) bool {
	//: pending, held answers ahead, or a file not yet gone.
	return res.Pending || len(f.held) > 0 || f.file.active()
}

// stepKind is what one turn of OnData decided.
type stepKind uint8

// connMode is how a connection's input is ending.
type connMode uint8

// LoopHandler serves the restricted HTTP/1.1 profile on a loop group: the
// corenet.LoopHandler HandleLoop returns. Its state is per lane — a request
// value, the batch's answers, a flag per connection — and a lane touches only
// its own, found by LoopIO.Loop and built at OnStart, so the data path takes
// no lock and, once a lane's buffers have grown to its batches, allocates
// nothing. It honours no group option itself: a loop group given
// ReadHeaderTimeout or MaxHeaderBytes refuses it at Start, the head bound
// being p's.
type LoopHandler struct {
	// h serves every request.
	h corenet.HTTP1Handler
	// batcher is h as an HTTP1Batcher, nil when it is not one.
	batcher corenet.HTTP1Batcher
	// worker is h as an HTTP1Worker, nil when it is not one: what a Pending
	// answer is offloaded to (R3).
	worker corenet.HTTP1Worker
	// upgrader is what an Upgrade answer hands the connection's bytes to; nil
	// when there is none.
	upgrader corenet.HTTP1Upgrader
	// streamer is what a Stream answer speaks to: h itself, or the handler a
	// chain of links ends in; nil when there is none.
	streamer corenet.HTTP1Streamer
	// canceller gives back a job no worker will run, nil when no link holds one.
	canceller jobCanceller
	// reader is h as an HTTP1BodyReader, nil when it takes bodies whole; aborter
	// is h as an HTTP1BodyAborter.
	reader  corenet.HTTP1BodyReader
	aborter corenet.HTTP1BodyAborter
	// holdCap bounds what one connection holds back behind a pending answer:
	// the group's OutputCap, given at OnOutbound.
	holdCap int
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
	// marked counts the requests an embedding adapter served in the batch
	// (MarkServed): the batch ends for them too.
	marked int
	// requests counts the lane's requests served since Start, added at each
	// batch's end, read by HTTPCounts from any goroutine.
	requests atomic.Uint64
	// opened are the connections whose stream's head this batch wrote: told of
	// their stream once it has left the adapter.
	opened []corenet.LoopConnRef
	// count numbers the lane's requests, from one: HTTP1RequestValue.ID's
	// counter.
	count uint64
	// outbound is the lane's outbound to its worker, nil when nothing
	// offloads.
	outbound corenet.LoopOutbound
	// slots are the pending answers' slots, by index; replaced, never written
	// in place, so the worker reads it without a lock.
	slots atomic.Pointer[[]*pendSlot]
	// freeSlots are the indexes of the slots no answer holds.
	freeSlots []uint32
	// ready are the connections whose held answers the batch's end flushes.
	ready []corenet.LoopConnRef
	// deferHeld are the connections holding a deferred answer behind a
	// pending one, completed after EndBatch.
	deferHeld []corenet.LoopConnRef
	// dropped are the held deferred responses a closed connection gave up
	// during the batch, returned to the pool after EndBatch.
	dropped []*corenet.HTTP1ResponseValue
	// held is the scratch a connection's held answers are written from.
	held []byte
	// unavailable is the 503 a pending answer refused by a full queue gets.
	unavailable corenet.HTTP1ResponseValue
	// copyRetry are the connections whose copied file's next chunk the worker's
	// full queue refused: sent again at the batch's end.
	copyRetry []corenet.LoopConnRef
	// readyEpoch numbers the batches' ready lists, from one.
	readyEpoch uint64
	// headStub and plainStub are the requests a held deferred answer is
	// written with: HEAD, or no method.
	headStub, plainStub corenet.HTTP1RequestValue
}

// dataCall is one OnData call's connection, what step and the body's two
// helpers read of it.
type dataCall struct {
	// io and ref are the callback's.
	io  corenet.LoopIO
	ref corenet.LoopConnRef
	// st is the lane's state, f the connection's flag, loop the lane.
	st   *loopState
	f    *connFlag
	loop int
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
	// mode is how the connection's input ends: read on, closed after the batch's
	// answer, or carried by a streamed response. In the last two nothing more is
	// read.
	mode connMode
	// dead is set once a write was refused or the close was asked: nothing
	// more is written to it.
	dead bool
	// upgradePending says the 101 is still in the batch's output: bytes after it
	// wait for it to have left.
	upgradePending bool
	// readyAt is the lane's readyEpoch the connection was last queued in,
	// so it is queued once a batch.
	readyAt uint64
	// file is a file answer whose body is still leaving (native or copy path):
	// every later answer waits behind it, in request order. It is allocated by
	// the slot's first file answer and kept for the slot's later connections,
	// as head and request are: a connection that never sends a file pays a
	// pointer, not the body's 48 bytes (the 512 B idle budget of #859).
	file *fileBody
	// held are the answers held behind a pending one, in request order (R3).
	held []heldItem
	// buf holds the held answers' bytes.
	buf []byte
	// bodyLeft is the bytes still to come of a body the handler reads as it
	// arrives (#844); zero when none is arriving.
	bodyLeft int64
	// head holds that request's head, and request the value framed from it:
	// both outlive OnData, and both are reused for the slot's next connection.
	head    []byte
	request *corenet.HTTP1RequestValue
}

// OnOpen implements corenet.LoopHandler. It marks the connection open on its lane.
func (a *LoopHandler) OnOpen(io corenet.LoopIO, ref corenet.LoopConnRef) {
	a.state(io.Loop()).flag(ref).reset(ref.Gen)
}

// endInput says nothing more is read from the connection; a stream keeps its
// own, stronger, ending.
func (f *connFlag) endInput() {
	//: a stream's mode is the one that says what to do on close.
	if f.mode != connStreaming {
		f.mode = connClosing
	}
}

// reset makes f a new connection's, of generation gen, keeping the backing
// of its held answers and their buffer, so a slot's churn reallocates
// neither.
func (f *connFlag) reset(gen uint32) {
	held, buf, head := f.held[:0], f.buf[:0], f.head[:0]
	file := f.file
	//: the body kept for reuse, emptied.
	if file != nil {
		*file = fileBody{}
	}
	*f = connFlag{gen: gen, held: held, buf: buf, head: head, request: f.request, file: file}
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
	if f.gen == ref.Gen && (f.mode == connClosing || f.mode == connStreaming) {
		//: everything is dropped with the connection.
		return len(buf)
	}
	used := 0
	c := dataCall{io: io, ref: ref, st: st, f: f, loop: loop}
	//: every whole request at the start of what is left.
	for used < len(buf) {
		n, next := a.step(c, buf[used:])
		used += n
		//: what the bytes decided.
		switch next {
		//: the answer closes the connection: the rest is never read.
		case stepClose:
			f.endInput()
			return len(buf)
		//: a partial request stays in the spill for its next bytes.
		case stepWait:
			return used
		case stepMore:
		}
	}
	//: the whole requests; a partial one stays in the spill.
	return used
}

// step frames, or feeds, at the start of buf, and reports the bytes it took and
// what to do next: a body still arriving takes the bytes before any next
// request, and a request is answered once whole, or read as it arrives when the
// handler takes its body so.
func (a *LoopHandler) step(c dataCall, buf []byte) (int, stepKind) {
	//: past a 101: the bytes are the upgraded protocol's, once its head has left.
	if c.f.mode == connUpgraded {
		return a.feedUpgraded(c, buf)
	}
	//: the rest of a body the handler reads as it arrives.
	if c.f.bodyLeft > 0 {
		return a.feedBody(c, buf)
	}
	st := c.st
	n, r := a.p.Parse(buf, &st.req)
	//: refused: answered by the codec, and the connection closed.
	if r != RefusalNone {
		off := len(st.out)
		st.out = AppendRefusal(st.out, r)
		st.items = append(st.items, item{ref: c.ref, off: off, end: len(st.out), closing: true})
		return 0, stepClose
	}
	//: the handler takes this body as it arrives: the head moves aside.
	if a.reader != nil && st.req.HeadLen > 0 && st.req.ContentLength > 0 {
		return a.openBody(c, buf), stepMore
	}
	//: a request still incomplete waits for its next bytes.
	if n == 0 {
		return 0, stepWait
	}
	//: the answer closes the connection: nothing past it is read.
	if a.serve(c.io, c.ref, st, c.loop) {
		return n, stepClose
	}
	return n, stepMore
}

// serve hands the request framed into st.req to the handler and queues its
// answer, and reports whether the connection closes after it.
func (a *LoopHandler) serve(io corenet.LoopIO, ref corenet.LoopConnRef, st *loopState, loop int) bool {
	st.req.Lane, st.req.Conn = loop, ref
	st.count++
	st.req.ID = uint64(loop)<<laneShift | st.count&counterMask
	res := st.take()
	st.served++
	a.h.ServeHTTP1(&st.req, res)
	closing := st.req.Close || res.Close || io.Pressure(ref)
	//: a stream or an upgrade: the head goes out with the batch, and the handler
	//: writes on.
	if res.Stream || res.Upgrade {
		//: nothing more of this connection is framed as HTTP.
		return a.openStream(ref, st, res)
	}
	closing = strayFile(res, closing)
	//: pending, or behind a pending answer or a file still leaving: held, in order (R3).
	if f := st.flag(ref); f.holds(res) {
		//: whether the connection closes after it.
		return a.hold(st, &answer{io: io, ref: ref, f: f, res: res, closing: closing})
	}
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
		out = appendInternalError(st.out, res)
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
func (a *LoopHandler) OnTimeout(io corenet.LoopIO, ref corenet.LoopConnRef) {
	a.timeout(io, ref)
}

// timeout tells a stream's handler of its deadline, or closes the connection.
func (a *LoopHandler) timeout(io corenet.LoopIO, ref corenet.LoopConnRef) {
	//: a stream's own timer.
	if f := a.state(io.Loop()).peek(ref); f != nil && (f.mode == connStreaming || f.mode == connUpgraded) {
		a.streamer.OnStreamTimeout(corenet.HTTP1StreamValue{IO: io, Ref: ref})
		//: told.
		return
	}
	//: a copied file's chunk that waited past its stall: the file goes first,
	//: whether or not the connection does.
	if f := a.state(io.Loop()).peek(ref); f != nil && f.file.active() && f.file.flags&bodyNative == 0 {
		a.endCopy(io, ref, a.state(io.Loop()), f, true)
		//: given up on.
		return
	}
	//: a stale reference is the only refusal, and means already closed.
	swallowErr(io.Close(ref))
}

// OnWritable implements corenet.LoopHandler. Nothing waits on it: an answer
// the output refuses closes its connection.
func (a *LoopHandler) OnWritable(io corenet.LoopIO, ref corenet.LoopConnRef) {
	//: a file still leaving is the only thing that waits on room.
	a.writable(io, ref)
}

// writable resumes a file body once its connection has room or its native send
// is over.
func (a *LoopHandler) writable(io corenet.LoopIO, ref corenet.LoopConnRef) {
	st := a.state(io.Loop())
	f := st.peek(ref)
	//: a file still leaving is the only thing that waits on room.
	if f != nil && f.file.active() {
		a.fileWritable(io, ref, st, f)
	}
}

// OnBatchEnd implements corenet.LoopHandler. When the batch served a request —
// its own, or one MarkServed reported —, it calls the handler's EndBatch if it
// has one, once, then writes the batch's answers in order and closes the
// connections whose answer asked to.
func (a *LoopHandler) OnBatchEnd(io corenet.LoopIO) {
	loop := io.Loop()
	st := a.state(loop)
	//: a batch with no answer, no held one ready and nothing served beside it
	//: has nothing to end.
	if st.idle() {
		//: nothing queued.
		return
	}
	//: the handler's batched work completes its deferred answers.
	if st.endServed() && a.batcher != nil {
		a.batcher.EndBatch(loop)
	}
	st.completeHeld()
	st.complete()
	st.flush(io)
	//: the held answers come after the batch's own, which precede them.
	a.flushReady(io, st)
	a.retryCopies(io, st)
	st.items, st.out = st.items[:0], st.out[:0]
	a.announceStreams(io, st)
}

// endServed counts the batch's own requests into the lane's total, forgets
// the batch's counts, and reports whether the batch served any request, its
// own or an embedding adapter's.
func (st *loopState) endServed() bool {
	ended := st.served+st.marked > 0
	//: one atomic add a batch, never one a request.
	if st.served > 0 {
		st.requests.Add(uint64(st.served))
	}
	st.served, st.marked = 0, 0
	//: whether the batch ends for the handler.
	return ended
}

// idle reports whether the lane's batch has nothing to end: no answer, no held
// one ready, nothing served beside it.
func (st *loopState) idle() bool {
	//: every queue the batch's end drains, and the requests served.
	return st.served+st.marked == 0 && len(st.items) == 0 && len(st.ready) == 0 && len(st.deferHeld) == 0 &&
		len(st.dropped) == 0 && len(st.opened) == 0 && len(st.copyRetry) == 0
}

// MarkServed tells the adapter that an adapter embedding it, serving another
// version on the same lane, served a request of lane's current batch: the
// next OnBatchEnd then calls the handler's EndBatch even when no HTTP/1.1
// request was served, and the embedding adapter does not call it, so a batch
// that served both versions ends once, as HTTP1Batcher's contract says. It is
// called on the lane's thread, before OnBatchEnd.
func (a *LoopHandler) MarkServed(lane int) {
	a.state(lane).marked++
}

// HTTPCounts implements corenet.LoopHTTPCounter: the HTTP/1.1 requests the
// adapter served, each lane's added at its batch's end; no HTTP/2.
func (a *LoopHandler) HTTPCounts() corenet.HTTPCountsValue {
	var v corenet.HTTPCountsValue
	//: every lane built so far.
	if p := a.loops.Load(); p != nil {
		//: a lane not yet seen has served nothing.
		for _, st := range *p {
			if st != nil {
				v.HTTP1Requests += st.requests.Load()
			}
		}
	}
	//: the sum.
	return v
}

// Unwrap returns the HTTP1Handler the adapter serves: what tells a loop group
// that its handler speaks the codec, so it may take TLS on the portable
// backend (ADR 0187 §D5).
func (a *LoopHandler) Unwrap() corenet.HTTP1Handler {
	//: the handler given to HandleLoop.
	return a.h
}

// OnStart implements corenet.LoopStarter: it builds every lane's state
// before the first callback, so no lane builds its own under a lock.
func (a *LoopHandler) OnStart(lanes int) {
	//: each lane's state, published at once; a lane past the count still
	//: builds its own, as a handler driven without OnStart does.
	for i := range lanes {
		a.state(i)
	}
}

// OnClose implements corenet.LoopHandler. The connection's held answers are
// given up — a pending one's job cancelled, its slot freed when it comes back
// — and its flag stays until the next OnOpen of its slot replaces it; an
// answer of the batch still queued for it is dropped when its Write is
// refused as stale.
func (a *LoopHandler) OnClose(io corenet.LoopIO, ref corenet.LoopConnRef, err error) {
	//: why it closed changes nothing here.
	swallowErr(err)
	//: the lane's own.
	a.closed(io, ref, err)
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
			next = appendInternalError(out, res)
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
		f.reset(ref.Gen)
	}
	//: the connection's own flag.
	return f
}

// peek returns ref's flag when it is ref's connection's, nil when the slot
// holds another generation or was never seen: unlike flag, it resets nothing.
func (st *loopState) peek(ref corenet.LoopConnRef) *connFlag {
	ring, slot := int(ref.Ring), int(ref.Slot)
	//: never seen.
	if ring >= len(st.conns) || slot >= len(st.conns[ring]) {
		//: none.
		return nil
	}
	f := &st.conns[ring][slot]
	//: another connection's.
	if f.gen != ref.Gen {
		//: none.
		return nil
	}
	//: the connection's own.
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
	st := &loopState{unavailable: corenet.HTTP1ResponseValue{Status: statusUnavailable, Header: retryAfter}, readyEpoch: 1}
	st.headStub.Method = methodHead
	st.req.Lane = i
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
