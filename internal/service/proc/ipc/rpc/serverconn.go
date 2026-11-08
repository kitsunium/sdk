package rpc

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	corerpc "github.com/kitsunium/sdk/internal/core/proc/ipc/rpc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// readBufferBytes is the buffered reader's size: a frame's header and a small
// body in one read.
const readBufferBytes int = 4 << 10

// serverConn is one connection a server serves: the reader on the goroutine
// that called ServeConn, the keeper on a second, and one goroutine per call
// in flight — MaxInFlight + 2 at most.
type serverConn struct {
	// srv is the server, for its configuration, router and count.
	srv *Server
	// conn is the connection and its peer.
	conn *coreipc.Conn
	// r buffers the connection's reads; only the reader uses it.
	r *bufio.Reader
	// keep times the frames and the calls' budgets.
	keep *keeper
	// format is the format the hello agreed on.
	format wireFormat
	// peerMax is the longest frame the client said it reads.
	peerMax int
	// ctx ends with the connection; every call's context derives from it.
	ctx context.Context
	// cancel ends ctx.
	cancel context.CancelCauseFunc
	// expired is set when a frame was not whole within FrameTimeout.
	expired atomic.Bool
	// once runs the teardown once.
	once sync.Once
	// wmu orders the frames written, and admission against a call's release:
	// a call's answer and its slot's release happen under it, so a client that
	// waits for an answer before its next call never finds the slot taken.
	wmu sync.Mutex
	// mu guards calls.
	mu sync.Mutex
	// calls are the calls in flight, by id.
	calls map[uint64]*serverCall
	// handlers counts the calls' goroutines, which serve waits for.
	handlers sync.WaitGroup
}

// serverCall is one call in flight, and the Request its handler reads.
type serverCall struct {
	// sc is the connection the call came on.
	sc *serverConn
	// id is the call's, chosen by the client.
	id uint64
	// method is the method called.
	method string
	// stream is set for a Stream, whose answer is items then an end.
	stream bool
	// body is the encoded value, after the budget.
	body []byte
	// ctx is the handler's context.
	ctx context.Context
	// cancel ends ctx, with the cause the handler reads.
	cancel context.CancelCauseFunc
	// abandoned is set when the client sent a cancel: nothing is answered.
	abandoned atomic.Bool
}

// newServerConn readies a connection for serve.
func newServerConn(s *Server, c *coreipc.Conn) *serverConn {
	ctx, cancel := context.WithCancelCause(context.Background())
	sc := &serverConn{
		srv: s, conn: c, r: bufio.NewReaderSize(c, readBufferBytes),
		ctx: ctx, cancel: cancel, calls: make(map[uint64]*serverCall),
	}
	sc.keep = newKeeper(s.clk, sc.frameExpired, sc.callExpired)
	return sc
}

// serve runs the hello, then reads and dispatches frames until the
// connection ends.
//
// Goroutine lifecycle: the keeper's, which ends with the teardown, and one
// per call admitted (start), each ending when its handler returns — at the
// latest once the teardown has ended its context. serve returns after both.
func (sc *serverConn) serve() error {
	var keeper sync.WaitGroup
	keeper.Go(sc.keep.run)
	defer keeper.Wait()
	defer sc.handlers.Wait()
	defer sc.teardown(connClosed())
	//: the hello comes first, within one FrameTimeout of the connection.
	if err := sc.hello(); err != nil {
		return err
	}
	for {
		f, err := readFrame(sc.r, sc.srv.cfg.MaxFrameBytes, sc.started)
		sc.keep.disarmRead()
		//: the connection ended, or the peer broke the protocol.
		if err != nil {
			return sc.readErr(err)
		}
		//: a frame out of place ends the connection.
		if err := sc.dispatch(f); err != nil {
			sc.refuse(err)
			return err
		}
	}
}

// hello reads the client's hello and answers it with this end's, or refuses
// it with FORMAT_REFUSED.
func (sc *serverConn) hello() error {
	sc.keep.armRead(sc.srv.cfg.FrameTimeout)
	f, err := readFrame(sc.r, sc.srv.cfg.MaxFrameBytes, sc.started)
	sc.keep.disarmRead()
	//: no hello at all.
	if err != nil {
		return sc.readErr(err)
	}
	peerMax, err := parseHello(f)
	//: a hello this end cannot answer.
	if err != nil {
		sc.refuse(err)
		return err
	}
	c, ok := sc.srv.codecs[codec.Format(f.method)]
	//: a format this server does not speak.
	if !ok {
		err := errs.Wrap(corerpc.FormatRefused, errs.WrapParams{}, errs.String("format", f.method))
		sc.refuse(err)
		return err
	}
	sc.format, sc.peerMax = wireFormat{c}, peerMax
	return sc.send(helloFrame(codec.Format(f.method), sc.srv.cfg.MaxFrameBytes), nil)
}

// started arms the frame's deadline once its first byte arrived.
func (sc *serverConn) started() {
	sc.keep.armRead(sc.srv.cfg.FrameTimeout)
}

// readErr says why reading stopped: nil for a peer that hung up between
// frames or a connection this end closed, the violation otherwise — told to
// the peer before the connection closes.
func (sc *serverConn) readErr(err error) error {
	switch {
	case sc.expired.Load():
		err = violation("a frame not whole within FrameTimeout")
	case errs.HasCode(err, corerpc.CodeFrameTooLarge), errs.HasCode(err, corerpc.CodeProtocolViolation):
	case err == io.EOF, sc.ctx.Err() != nil:
		return nil
	case err == io.ErrUnexpectedEOF:
		err = violation("a frame cut short")
	default:
		return connClosedBy(err)
	}
	sc.refuse(err)
	return err
}

// refuse tells the peer why the connection closes, best effort: the
// connection may already be gone.
func (sc *serverConn) refuse(err error) {
	swallowErr(sc.send(errorFrame(0, err), nil))
}

// dispatch acts on one frame.
func (sc *serverConn) dispatch(f frameValue) error {
	switch f.kind {
	case kindCall, kindStream:
		return sc.start(f)
	case kindCancel:
		sc.abandon(f.id)
		return nil
	case kindPing:
		return sc.send(emptyFrame(kindPong, f.id), nil)
	default:
		return violation("a frame kind a client does not send")
	}
}

// start admits a call and runs its handler, or answers METHOD_UNKNOWN or
// OVERLOADED at once.
//
// Goroutine lifecycle: one per call admitted, counted by handlers; it ends
// when the handler returns and the answer is written, and serve waits for it.
func (sc *serverConn) start(f frameValue) error {
	//: a call has an id and opens with its budget.
	if f.id == 0 || len(f.body) < budgetBytes {
		return violation("a call without an id or a budget")
	}
	h, ok := sc.srv.router.Route(f.method)
	//: answered at once; no slot is taken.
	if !ok {
		return sc.send(errorFrame(f.id, errs.Wrap(corerpc.MethodUnknown, errs.WrapParams{},
			errs.String("method", f.method))), nil)
	}
	sc.wmu.Lock()
	call, refusal, fatal := sc.admit(f)
	sc.wmu.Unlock()
	//: a reused id breaks the protocol.
	if fatal != nil {
		return fatal
	}
	//: the slots are full: OVERLOADED, never queued.
	if refusal != nil {
		return sc.send(errorFrame(f.id, refusal), nil)
	}
	//: counted before it starts, so serve's Wait never misses it; a method
	//: value, not a closure, so a call costs no allocation for its goroutine.
	sc.handlers.Add(1)
	go call.run(h)
	return nil
}

// admit takes a slot for the call; the caller holds wmu, so a call answering
// right now has released its slot first. refusal is OVERLOADED, fatal a
// reused id.
func (sc *serverConn) admit(f frameValue) (call *serverCall, refusal, fatal error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	//: an id is the client's, once at a time.
	if _, dup := sc.calls[f.id]; dup {
		return nil, nil, violation("an id reused while in flight")
	}
	//: MaxInFlight handlers already run.
	if len(sc.calls) >= sc.srv.cfg.MaxInFlight {
		return nil, errs.Wrap(corerpc.Overloaded, errs.WrapParams{},
			errs.Int("max_in_flight", sc.srv.cfg.MaxInFlight)), nil
	}
	ctx, cancel := context.WithCancelCause(sc.ctx)
	call = &serverCall{
		sc: sc, id: f.id, method: f.method, stream: f.kind == kindStream,
		body: f.body[budgetBytes:], ctx: ctx, cancel: cancel,
	}
	sc.calls[f.id] = call
	sc.srv.inflight.Add(1)
	//: the caller's deadline, on this server's clock.
	if budget := binary.BigEndian.Uint64(f.body); budget > 0 {
		sc.keep.armCall(f.id, budgetDuration(budget))
	}
	return call, nil, nil
}

// budgetDuration is a budget in nanoseconds as a duration, the longest one
// standing for anything past it.
func budgetDuration(ns uint64) time.Duration {
	//: past what a duration holds.
	if ns > math.MaxInt64 {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(ns)
}

// abandon ends the context of a call its client cancelled; its answer is
// not sent. A cancel for a call that already answered changes nothing.
func (sc *serverConn) abandon(id uint64) {
	sc.mu.Lock()
	call := sc.calls[id]
	sc.mu.Unlock()
	//: the call answered before the cancel arrived.
	if call == nil {
		return
	}
	call.abandoned.Store(true)
	call.cancel(errs.Wrap(corerpc.CallCancelled, errs.WrapParams{}, errs.String("by", "the client")))
}

// callExpired ends the context of a call whose budget passed.
func (sc *serverConn) callExpired(id uint64) {
	sc.mu.Lock()
	call := sc.calls[id]
	sc.mu.Unlock()
	//: it answered meanwhile.
	if call == nil {
		return
	}
	call.cancel(errs.Wrap(corerpc.CallTimeout, errs.WrapParams{}, errs.String("by", "the budget")))
}

// frameExpired closes a connection a frame was not whole on in time: the
// reader, or the writer, stops at once.
func (sc *serverConn) frameExpired() {
	sc.expired.Store(true)
	sc.teardown(violation("a frame not whole within FrameTimeout"))
}

// send writes one frame — under wmu, within FrameTimeout — then runs after,
// still under wmu: a call's slot is released there.
func (sc *serverConn) send(frame []byte, after func()) error {
	sc.wmu.Lock()
	defer sc.wmu.Unlock()
	var err error
	//: an abandoned call answers nothing, and still releases its slot.
	if frame != nil {
		sc.keep.armWrite(sc.srv.cfg.FrameTimeout)
		_, err = sc.conn.Write(frame)
		sc.keep.disarmWrite()
	}
	//: the release happens whether the write did or not.
	if after != nil {
		after()
	}
	//: a connection that cannot be written is over.
	if err != nil {
		sc.teardown(connClosedBy(err))
		return connClosedBy(err)
	}
	return nil
}

// release frees a call's slot, its deadline and its context.
func (sc *serverConn) release(call *serverCall) {
	sc.mu.Lock()
	delete(sc.calls, call.id)
	sc.mu.Unlock()
	sc.keep.disarmCall(call.id)
	call.cancel(nil)
	sc.srv.inflight.Add(-1)
}

// teardown ends the connection once: every call's context with cause, the
// keeper, and the connection itself.
func (sc *serverConn) teardown(cause error) {
	sc.once.Do(func() {
		sc.cancel(cause)
		sc.keep.close()
		swallowErr(sc.conn.Close())
	})
}

// run is a call's goroutine: the handler, then the answer and the release;
// serve waits for it through handlers.
func (c *serverCall) run(h corerpc.Handler) {
	defer c.sc.handlers.Done()
	reply, err := h.ServeRPC(c.ctx, c)
	//: a stream's return value is one last item.
	if err == nil && c.stream && reply != nil {
		err = c.Send(reply)
	}
	var frame []byte
	//: a call's return value is its reply, encoded into its frame.
	if err == nil && !c.stream {
		frame, err = c.sc.format.Append(c.head(kindReply), reply)
	}
	swallowErr(c.sc.send(c.answer(frame, err), func() { c.sc.release(c) }))
}

// answer is the frame that answers the call — the reply built, the stream's
// end, or the error —, nil when its client abandoned it.
func (c *serverCall) answer(reply []byte, err error) []byte {
	//: the client is no longer waiting.
	if c.abandoned.Load() {
		return nil
	}
	//: an error of the handler's — or, when it gave up with the context's
	//: error, the cause that ended the context.
	if err != nil {
		return errorFrame(c.id, c.cause(err))
	}
	//: a stream's items were sent: its end.
	if c.stream {
		return emptyFrame(kindEnd, c.id)
	}
	frame, err := finishFrame(reply, c.sc.peerMax)
	//: the client could not read it.
	if err != nil {
		return errorFrame(c.id, err)
	}
	return frame
}

// cause is err, or — when err carries no SDK code and the context ended —
// the cause that ended it: CALL_TIMEOUT, CALL_CANCELLED or CONN_CLOSED.
func (c *serverCall) cause(err error) error {
	//: the handler's own SDK error, or a context still running.
	if _, ok := errs.CodeOf(err); ok || c.ctx.Err() == nil {
		return err
	}
	return context.Cause(c.ctx)
}

// head is a frame of kind for this call, its body still to append.
func (c *serverCall) head(kind byte) []byte {
	return beginFrame(make([]byte, 0, frameStartBytes), kind, c.id, "")
}

// Method is the method called.
func (c *serverCall) Method() string {
	return c.method
}

// Peer is who called, as the private socket's kernel named it.
func (c *serverCall) Peer() coreipc.PeerValue {
	return c.sc.conn.Peer
}

// Decode decodes the call's body into v with the connection's format.
func (c *serverCall) Decode(v any) error {
	//: the body is not a value of v's shape in this format.
	if err := c.sc.format.Unmarshal(c.body, v); err != nil {
		return codecFailed("decode", err)
	}
	return nil
}

// Send writes item to a Stream's caller.
func (c *serverCall) Send(item any) error {
	//: a Call is answered once, by the handler's return.
	if !c.stream {
		return violation("Send on a call that is not a stream")
	}
	//: the client cancelled, its budget passed, or the connection closed.
	if c.ctx.Err() != nil {
		return context.Cause(c.ctx)
	}
	frame, err := c.sc.format.Append(c.head(kindItem), item)
	//: the format refused the item.
	if err != nil {
		return err
	}
	frame, err = finishFrame(frame, c.sc.peerMax)
	//: too long for the client: nothing is written.
	if err != nil {
		return err
	}
	return c.sc.send(frame, nil)
}

// connClosed is CONN_CLOSED, for a connection this end closed.
func connClosed() error {
	return errs.Wrap(corerpc.ConnClosed, errs.WrapParams{}, errs.String("by", "this end"))
}

// connClosedBy is CONN_CLOSED for a connection that failed, the failure in
// the log fields.
func connClosedBy(cause error) error {
	return errs.Wrap(corerpc.ConnClosed, errs.WrapParams{}, errs.String("cause", cause.Error()))
}
