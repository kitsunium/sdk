package http2

import (
	"bytes"
	"errors"
	"io"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// The statuses the adapter answers by itself.
const (
	// statusContentTooLarge answers a body past its bound.
	statusContentTooLarge int = 413
	// statusInternal answers a response the writer refused.
	statusInternal int = 500
)

// A request's ID, as http1's: the lane on top, a counter below, with one bit
// apart so an HTTP/2 request never shares an ID with an HTTP/1.1 one of its lane.
const (
	// idLaneShift places the lane in an ID's top 16 bits.
	idLaneShift uint = 48
	// idHTTP2Bit sets HTTP/2 apart.
	idHTTP2Bit uint64 = 1 << 47
	// idCounterMask keeps the counter under idHTTP2Bit.
	idCounterMask uint64 = idHTTP2Bit - 1
)

// methodHEAD is the one method whose answer has no body.
var methodHEAD = []byte("HEAD")

// h2Call is one callback's connection, what the stream handlers read of it.
type h2Call struct {
	// a is the adapter.
	a *LoopHandler
	// io and ref are the callback's.
	io  corenet.LoopIO
	ref corenet.LoopConnRef
	// st is the lane's state, e the connection's entry.
	st *laneState
	e  *connEntry
}

// feed runs Feed over buf and serves what it reports, and returns the bytes
// it took: a partial frame stays in the spill.
func (a *LoopHandler) feed(io corenet.LoopIO, ref corenet.LoopConnRef, st *laneState, e *connEntry, buf []byte) int {
	off := len(st.out)
	n, out := st.lane.Feed(&e.conn, buf, st.out)
	st.out = out
	c := h2Call{a: a, io: io, ref: ref, st: st, e: e}
	//: the frames' events, in order.
	for _, ev := range st.lane.Events() {
		c.event(ev)
	}
	//: the h2 reading of Connection: close: one GOAWAY, the streams finish.
	if io.Pressure(ref) {
		c.goAway()
	}
	st.pushWrite(ref, off, e)
	//: the frames Feed took.
	return n
}

// pushWrite records what the batch has appended since off as ref's, and marks
// the connection active once per batch.
func (st *laneState) pushWrite(ref corenet.LoopConnRef, off int, e *connEntry) {
	//: only a range that holds bytes costs a write.
	if len(st.out) > off {
		st.writes = append(st.writes, write{ref: ref, off: off, end: len(st.out)})
	}
	//: once per batch.
	if e.activeAt != st.lane.epoch {
		e.activeAt = st.lane.epoch
		st.active = append(st.active, ref)
	}
}

// event serves one event of Feed.
func (c *h2Call) event(ev Event) {
	//: by what the frame was.
	switch ev.Kind {
	case EventHeaders:
		c.onHeaders(ev)
	case EventData:
		c.onData(ev)
	case EventReset:
		c.onReset(ev)
	default:
		//: a peer's GOAWAY: it opens no new stream, and Done closes when
		//: the streams end.
	}
}

// onHeaders decodes a field block and opens, refuses or serves its request. A
// block on a stream the machine refused is decoded and dropped.
func (c *h2Call) onHeaders(ev Event) {
	st := c.st
	var err error
	st.scratch, err = st.lane.Decode(&c.e.conn, ev.Data, st.scratch, &st.req)
	//: a field block the table does not survive ends the connection; a
	//: malformed one ends only its stream.
	if err != nil && !errors.Is(err, corehttp2.FieldMalformed) {
		st.countAbort(err)
		st.out = st.lane.Abort(&c.e.conn, corehttp2.ErrCodeCompression, st.out)
		//: the connection ends.
		return
	}
	//: decoded for the table's sake only.
	if ev.Flags&EventDiscard != 0 {
		//: dropped.
		return
	}
	//: a malformed field block: the stream ends here.
	if err != nil {
		st.errs[corehttp2.ErrorTypeProtocol]++
		st.out = st.lane.ResetStream(&c.e.conn, ev.Stream, corehttp2.ErrCodeProtocol, st.out)
		//: its stream only.
		return
	}
	//: the stream a later frame of this same read already ended: the handler
	//: must not pay for it, or a Rapid Reset would buy it for free.
	if idx, _ := st.lane.find(&c.e.conn, ev.Stream); idx == noIndex {
		//: dropped with the reset.
		return
	}
	c.open(ev.Stream, ev.Flags&EventEndStream != 0)
}

// open maps a decoded request and serves or defers it: at once when it carries
// no body, otherwise a record keeps it until its last DATA.
func (c *h2Call) open(stream uint32, end bool) {
	st := c.st
	req := &st.req
	verdict := mapRequest(req, st, stream)
	//: by what the mapping found.
	switch {
	case verdict.malformed:
		st.errs[corehttp2.ErrorTypeProtocol]++
		st.out = st.lane.ResetStream(&c.e.conn, stream, corehttp2.ErrCodeProtocol, st.out)
	case verdict.refusal != http1.RefusalNone:
		c.answerStatus(stream, verdict.refusal.Status())
	case end:
		c.serve(stream, req, noIndex, noIndex)
	default:
		c.keep(stream, verdict)
	}
}

// keep copies the decoded request into a record of its own, which outlives
// this batch, for its body to arrive.
func (c *h2Call) keep(stream uint32, v mapVerdict) {
	st := c.st
	_, r := st.takeRequest(c.e, stream)
	r.head = copyRequest(&r.req, &st.req, r.head)
	r.wanted = v.wanted
	//: what the head said.
	if v.declared {
		r.flags |= requestDeclared
	}
	//: a HEAD's answer carries no body.
	if bytes.Equal(r.req.Method, methodHEAD) {
		r.flags |= requestHead
	}
}

// onData accumulates a stream's body, serves the request on its last byte,
// and refuses a body past its bound or its declared length.
func (c *h2Call) onData(ev Event) {
	st := c.st
	idx, prev := st.findRequest(c.e, ev.Stream)
	//: bytes for a stream the adapter is done with: the connection's credit
	//: alone is owed.
	if idx == noIndex {
		st.lane.Consume(&c.e.conn, ev.Stream, len(ev.Data))
		//: nobody reads them.
		return
	}
	r := st.requests[idx]
	//: answered early: nobody reads the rest.
	if r.has(requestRefused) {
		st.lane.Consume(&c.e.conn, ev.Stream, len(ev.Data))
		//: dropped.
		return
	}
	//: past the body bound: 413, the codec reports it once.
	if ev.Flags&EventOverBody != 0 {
		c.refuseBody(idx, prev, statusContentTooLarge)
		//: refused.
		return
	}
	//: a body longer than its Content-Length is malformed, §8.1.1.
	if r.has(requestDeclared) && r.received()+int64(len(ev.Data)) > r.wanted {
		c.resetMalformed(idx, prev)
		//: refused.
		return
	}
	c.take(idx, prev, r, ev)
}

// received is the body bytes the record has seen so far.
func (r *requestState) received() int64 {
	//: a handler that takes its body as it arrives counts its chunks.
	return int64(len(r.body)) + r.chunks
}

// take gives one DATA event to the handler that reads as it arrives, or
// accumulates it, and serves the request on its end.
func (c *h2Call) take(idx, prev int32, r *requestState, ev Event) {
	st := c.st
	end := ev.Flags&EventEndStream != 0
	//: a handler that takes its body as it arrives: the chunk, and credit.
	if c.a.reader != nil {
		r.chunks += int64(len(ev.Data))
		r.flags |= requestReading
		c.a.reader.OnBody(&r.req, ev.Data, end)
		st.lane.Consume(&c.e.conn, ev.Stream, len(ev.Data))
	} else {
		c.buffer(r, ev)
	}
	//: the body's last byte: a declared length must have been met.
	if end {
		//: fewer bytes than announced, §8.1.1.
		if r.has(requestDeclared) && r.received() != r.wanted {
			c.resetMalformed(idx, prev)
			//: refused.
			return
		}
		c.serve(ev.Stream, &r.req, idx, prev)
	}
}

// buffer appends a chunk to the record's body, crediting it at once while the
// connection's buffered bodies stay under the receive bound: past it the
// credit waits for the answer, so the peer's flow control stops what memory
// would otherwise not.
func (c *h2Call) buffer(r *requestState, ev Event) {
	st := c.st
	r.body = append(r.body, ev.Data...)
	c.e.buffered += int32(len(ev.Data))
	//: within the connection's bound: credited now.
	if int(c.e.buffered) <= st.lane.limits.ReceivePerConn {
		st.lane.Consume(&c.e.conn, ev.Stream, len(ev.Data))
	} else {
		r.uncredited += int32(len(ev.Data))
	}
}

// refuseBody answers status on the stream, its body dropped.
func (c *h2Call) refuseBody(idx, prev int32, status int) {
	st := c.st
	r := st.requests[idx]
	r.flags |= requestRefused
	//: a body the handler was reading: it will not arrive whole.
	if r.has(requestReading) && c.a.aborter != nil {
		c.a.aborter.AbortBody(&r.req)
	}
	c.answerStatus(r.stream, status)
	st.finishRequest(c.e, idx, prev)
}

// resetMalformed ends the stream with RST_STREAM(PROTOCOL_ERROR), its body
// dropped.
func (c *h2Call) resetMalformed(idx, prev int32) {
	st := c.st
	r := st.requests[idx]
	//: a body the handler was reading: it will not arrive whole.
	if r.has(requestReading) && c.a.aborter != nil {
		c.a.aborter.AbortBody(&r.req)
	}
	st.errs[corehttp2.ErrorTypeProtocol]++
	st.out = st.lane.ResetStream(&c.e.conn, r.stream, corehttp2.ErrCodeProtocol, st.out)
	st.finishRequest(c.e, idx, prev)
}

// onReset forgets a stream the peer or the state machine ended: its record is
// dropped and a worker's answer cancelled, its slot freed when it comes back.
func (c *h2Call) onReset(ev Event) {
	st := c.st
	idx, prev := st.findRequest(c.e, ev.Stream)
	//: answered already, or never opened here.
	if idx == noIndex {
		//: nothing to forget.
		return
	}
	r := st.requests[idx]
	//: the stream's body will not arrive whole.
	if r.has(requestReading) && c.a.aborter != nil {
		c.a.aborter.AbortBody(&r.req)
	}
	//: a worker still owes it: dropped when the job comes back.
	if r.slot != noIndex {
		(*st.slots.Load())[r.slot].cancelled.Store(true)
	}
	st.finishRequest(c.e, idx, prev)
}

// serve hands a whole request to h and answers, defers or offloads as it asks.
// idx is the request's record, or noIndex when it never needed one.
func (c *h2Call) serve(stream uint32, req *corenet.HTTP1RequestValue, idx, prev int32) {
	st := c.st
	res := st.take()
	st.served++
	req.Lane, req.Conn = st.index, c.ref
	st.count++
	req.ID = uint64(st.index)<<idLaneShift | idHTTP2Bit | st.count&idCounterMask
	//: a handler that took the body as it arrived has nothing to read here.
	if idx != noIndex && c.a.reader == nil {
		req.Body = st.requests[idx].body
	}
	c.a.h.ServeHTTP1(req, res)
	closing := res.Close || c.io.Pressure(c.ref)
	idx = c.dispatch(stream, res, bytes.Equal(req.Method, methodHEAD), idx)
	//: the h2 reading of Connection: close.
	if closing {
		c.goAway()
	}
	//: an answer that is out: the record is done; a pending one keeps its own.
	if idx != noIndex && st.requests[idx].slot == noIndex {
		st.finishRequest(c.e, idx, prev)
	}
}

// dispatch takes the handler's answer: at once, at the batch's end, or by the
// worker. It returns the record that stays for a worker.
func (c *h2Call) dispatch(stream uint32, res *corenet.HTTP1ResponseValue, isHead bool, idx int32) int32 {
	st := c.st
	//: by what the handler asked.
	switch {
	case res.Pending:
		idx = c.offload(stream, res, isHead, idx)
		st.give(res)
	case res.Defer:
		st.deferred = append(st.deferred, deferredAnswer{res: res, ref: c.ref, stream: stream, isHead: isHead})
	default:
		c.answer(stream, res, isHead)
		st.give(res)
	}
	//: the record, as it stands.
	return idx
}

// answer writes res on stream: HEADERS, then DATA unless it is a HEAD or empty.
// A response the writer refuses is answered 500.
func (c *h2Call) answer(stream uint32, res *corenet.HTTP1ResponseValue, isHead bool) {
	st := c.st
	//: a form of answer the HTTP/2 profile does not carry: a 500, as a
	//: response the writer refuses is.
	if unservable(res) {
		c.answerStatus(stream, statusInternal)
		//: answered.
		return
	}
	body := res.Body
	endOnHeaders := len(body) == 0 || isHead
	out, err := st.lane.Headers(&c.e.conn, stream, res, endOnHeaders, st.out)
	//: refused by the writer: a 500 on the stream, the connection kept.
	if err != nil {
		c.answerStatus(stream, statusInternal)
		//: answered.
		return
	}
	st.out = out
	//: the body, as far as the windows allow; the rest is held.
	if !endOnHeaders {
		st.out = st.lane.Send(&c.e.conn, stream, body, st.out, true)
	}
}

// unservable reports an answer HTTP/2 does not carry on the loop: a
// close-delimited stream, an Upgrade or a file body; a file's reader is
// closed, as http1 closes one it does not send.
func unservable(res *corenet.HTTP1ResponseValue) bool {
	//: a reader the answer will not read.
	if c, ok := res.File.R.(io.Closer); ok && res.File.R != nil {
		swallowErr(c.Close())
	}
	//: any of the three.
	return res.Stream || res.Upgrade || res.File.R != nil
}

// answerStatus writes a bodiless answer of status on stream, ending it; a
// request whose body is still arriving is reset with NO_ERROR by the codec.
func (c *h2Call) answerStatus(stream uint32, status int) {
	st := c.st
	res := &corenet.HTTP1ResponseValue{Status: status}
	out, err := st.lane.Headers(&c.e.conn, stream, res, true, st.out)
	//: every status it writes is sendable; the guard is the writer's refusal.
	if err == nil {
		st.out = out
	}
}

// goAway writes GOAWAY(NO_ERROR) once: the connection serves its streams and
// closes when they end.
func (c *h2Call) goAway() {
	//: one GOAWAY a connection, whoever asked.
	if c.e.flags&entryGoAway != 0 {
		//: already written.
		return
	}
	c.e.flags |= entryGoAway
	c.st.out = c.st.lane.GoAway(&c.e.conn, corehttp2.ErrCodeNoError, c.st.out)
}

// OnBatchEnd implements corenet.LoopHandler. It runs the handler's EndBatch,
// writes the batch's answers, credits the connections it fed, and closes
// those that ended.
func (a *LoopHandler) OnBatchEnd(io corenet.LoopIO) {
	st := a.state(io.Loop())
	//: HTTP/2 served too: the embedded adapter ends the batch for both.
	if st.served > 0 {
		a.h1.MarkServed(st.index)
	}
	a.h1.OnBatchEnd(io)
	//: nothing fed: an empty iteration of the loop.
	if len(st.active) == 0 && len(st.writes) == 0 && len(st.deferred) == 0 {
		//: nothing to end.
		return
	}
	a.completeDeferred(io, st)
	a.flush(io, st)
	a.publish(st)
	st.endBatch()
}

// publish adds the batch's requests and errors to the adapter's counts: one
// atomic add a batch for the requests, one a type counted for the errors.
func (a *LoopHandler) publish(st *laneState) {
	//: a batch of HTTP/1.1 alone touches nothing shared.
	if st.served > 0 {
		a.requests.Add(uint64(st.served))
	}
	st.lane.TakeErrors(st.errs[:])
	//: the types the batch counted.
	for i, n := range st.errs {
		if n > 0 {
			a.errs[i].Add(n)
			st.errs[i] = 0
		}
	}
}

// countAbort counts a field block Decode refused: a header list past its
// bound, or a block HPACK could not decode.
func (st *laneState) countAbort(err error) {
	//: the bound, which RFC 9113 spells as no code of its own.
	if errors.Is(err, corehttp2.HeadersTooLarge) {
		st.errs[corehttp2.ErrorTypeHeaderList]++
		return
	}
	st.errs[corehttp2.ErrorTypeCompression]++
}

// HTTPCounts implements corenet.LoopHTTPCounter: the requests served per
// version — the HTTP/1.1 ones the embedded adapter's —, and the HTTP/2 errors
// the codec answered with, each lane's added up at the batch's end.
func (a *LoopHandler) HTTPCounts() corenet.HTTPCountsValue {
	v := a.h1.HTTPCounts()
	v.HTTP2Requests = a.requests.Load()
	//: a row for each type counted, in the vocabulary's order.
	for i := range a.errs {
		if n := a.errs[i].Load(); n > 0 {
			v.HTTP2Errors = append(v.HTTP2Errors, corehttp2.ErrorCountValue{Type: corehttp2.ErrorType(i), Count: n})
		}
	}
	//: the sum.
	return v
}

// completeDeferred appends the batch's deferred answers now that EndBatch has
// run; one still deferred is answered 500.
func (a *LoopHandler) completeDeferred(io corenet.LoopIO, st *laneState) {
	//: each deferred answer, on its own connection.
	for i := range st.deferred {
		d := &st.deferred[i]
		e := st.entry(d.ref)
		//: the connection went away during the batch.
		if !e.live() || e.gen != d.ref.Gen {
			st.give(d.res)
			continue
		}
		c := h2Call{a: a, io: io, ref: d.ref, st: st, e: e}
		off := len(st.out)
		//: left deferred by EndBatch: a 500 on its stream.
		if d.res.Defer {
			d.res.Status = statusInternal
			d.res.Header, d.res.Body = nil, nil
		}
		c.answer(d.stream, d.res, d.isHead)
		st.give(d.res)
		st.pushWrite(d.ref, off, e)
	}
	st.deferred = st.deferred[:0]
}

// flush credits the batch's connections and writes its output in order, then
// closes the connections that ended.
func (a *LoopHandler) flush(io corenet.LoopIO, st *laneState) {
	st.credit()
	st.writeAll(io)
	//: the connections that ended, once their last bytes have been written.
	for _, ref := range st.active {
		a.closeIfDone(io, ref, st)
	}
}

// credit appends the batch's window credits, one frame per stream at most.
func (st *laneState) credit() {
	//: the connections the batch fed.
	for _, ref := range st.active {
		e := st.entry(ref)
		//: a connection that closed during the batch owes no credit.
		if !e.live() || e.gen != ref.Gen {
			continue
		}
		off := len(st.out)
		st.out = st.lane.AppendCredits(&e.conn, st.out)
		st.pushWrite(ref, off, e)
	}
}

// writeAll writes the batch's output in the order it was made; a refused
// write ends its connection, as a refused http1 answer does.
func (st *laneState) writeAll(io corenet.LoopIO) {
	//: range by range.
	for _, w := range st.writes {
		e := st.entry(w.ref)
		//: closed meanwhile: nothing more is written to it.
		if !e.live() || e.gen != w.ref.Gen {
			continue
		}
		//: the connection's output refused it.
		if err := io.Write(w.ref, st.out[w.off:w.end]); err != nil {
			e.flags |= entryEnded
		}
	}
}

// closeIfDone closes ref when its output was refused, a protocol error ended
// it, or a GOAWAY went out and no stream is left open.
func (*LoopHandler) closeIfDone(io corenet.LoopIO, ref corenet.LoopConnRef, st *laneState) {
	e := st.entry(ref)
	//: closed meanwhile.
	if !e.live() || e.gen != ref.Gen {
		//: nothing to close.
		return
	}
	//: ended, or nothing more to serve.
	if e.flags&entryEnded != 0 || st.lane.Closing(&e.conn) || st.lane.Done(&e.conn) {
		//: a stale reference is the only refusal, and means already closed.
		swallowErr(io.Close(ref))
	}
}

// endBatch ends the lane's batch: the output and scratches cleared, the
// protocol state's batch ended, the requests answered returned to the pool.
func (st *laneState) endBatch() {
	st.lane.EndBatch()
	st.out, st.writes, st.active = st.out[:0], st.writes[:0], st.active[:0]
	st.scratch, st.served = st.scratch[:0], 0
	//: the requests the batch answered, now that their bytes are done with.
	for _, idx := range st.completed {
		st.release(idx)
	}
	st.completed = st.completed[:0]
}
