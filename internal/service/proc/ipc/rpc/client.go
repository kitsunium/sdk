package rpc

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	corerpc "github.com/kitsunium/sdk/internal/core/proc/ipc/rpc"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// streamBuffer is how many of a stream's frames wait for its reader before
// the connection's reader waits too.
const streamBuffer int = 16

// ClientConfig is how a client speaks and how long it waits. Every bound is
// the caller's decision: none has a default, and one that is not positive is
// refused (ADR 0031).
type ClientConfig struct {
	// Format is the body format, by the name a codec is registered under.
	Format codec.Format
	// MaxFrameBytes is the longest frame the client reads, checked on the
	// length before anything is read; the server is told it in the hello.
	MaxFrameBytes int
	// MaxInFlight is how many calls and streams the client keeps open at once;
	// the next is OVERLOADED, before a byte is sent.
	MaxInFlight int
	// FrameTimeout is how long a frame may take, from its first byte to its
	// last, read or written; past it the connection is closed.
	FrameTimeout time.Duration
	// CallTimeout is the longest a Call or a Ping waits, on Clock, when its
	// context allows longer; a Stream lives as long as its context.
	CallTimeout time.Duration
	// Clock times the frames and the calls; nil is clock.System.
	Clock clock.Timed
}

// Client is one connection to a server, safe for concurrent use: its calls
// and streams share the connection, each under its own id. It is the engine
// behind the core Caller port.
type Client struct {
	// cfg is the validated configuration.
	cfg ClientConfig
	// format is the format's codec, as an Appender.
	format wireFormat
	// clk is the injected clock.
	clk clock.Timed
	// conn is the connection and the server's identity.
	conn *coreipc.Conn
	// r buffers the connection's reads; only the reader uses it.
	r *bufio.Reader
	// keep times the frames read and written.
	keep *keeper
	// peerMax is the longest frame the server said it reads.
	peerMax int
	// expired is set when a frame was not whole within FrameTimeout.
	expired atomic.Bool
	// done is closed when the connection ends; err says why.
	done chan struct{}
	// once closes done once.
	once sync.Once
	// wmu orders the frames written.
	wmu sync.Mutex
	// mu guards what follows.
	mu sync.Mutex
	// err is why the connection ended, set before done closes.
	err error
	// lastID is the last id issued; ids start at 1.
	lastID uint64
	// pending are the calls and streams waiting for an answer, by id.
	pending map[uint64]*pendingCall
}

// pendingCall is a call, a ping or a stream waiting for its answer.
type pendingCall struct {
	// answers receives the call's answer, or a stream's items and its end.
	answers chan frameValue
	// gone is closed when a stream's reader abandoned it: the reader drops
	// what still comes for it rather than wait. A call has none.
	gone chan struct{}
	// stream is set for a stream, whose answer is many frames.
	stream bool
}

// NewClient validates cfg, dials d within ctx and says hello: the format, the
// protocol version and the largest frame each end accepts. A refused hello is
// FORMAT_REFUSED; a dial that fails is the dialer's error.
//
// Goroutine lifecycle: the client runs two, the connection's reader and its
// keeper; both end when the connection does — Close, a broken connection, or
// a peer that broke the protocol.
func NewClient(ctx context.Context, d coreipc.Dialer, cfg ClientConfig) (*Client, error) {
	cc, err := clientCodec(cfg)
	//: a configuration no call can run with.
	if err != nil {
		return nil, err
	}
	conn, err := d.Dial(ctx)
	//: nobody to talk to.
	if err != nil {
		return nil, err
	}
	c := &Client{
		cfg: cfg, format: wireFormat{cc}, clk: clockOr(cfg.Clock), conn: conn,
		r: bufio.NewReaderSize(conn, readBufferBytes), done: make(chan struct{}),
		pending: make(map[uint64]*pendingCall),
	}
	c.keep = newKeeper(c.clk, c.frameExpired, func(uint64) {})
	go c.keep.run()
	//: a hello the server refused, or no hello at all.
	if err := c.hello(ctx); err != nil {
		c.fail(err)
		return nil, err
	}
	go c.read()
	return c, nil
}

// hello says this end's format, version and largest frame, and reads the
// server's — within FrameTimeout, and within ctx.
func (c *Client) hello(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { swallowErr(c.conn.Close()) })
	defer stop()
	//: the hello could not be written.
	if err := c.write(helloFrame(c.cfg.Format, c.cfg.MaxFrameBytes)); err != nil {
		return err
	}
	c.keep.armRead(c.cfg.FrameTimeout)
	f, err := readFrame(c.r, c.cfg.MaxFrameBytes, c.started)
	c.keep.disarmRead()
	//: the context ended, or no hello came.
	if err != nil {
		return c.helloErr(ctx, err)
	}
	//: the server refused this end's hello.
	if f.kind == kindError {
		return remoteError(f)
	}
	peerMax, err := parseHello(f)
	//: an answer that is not a hello.
	if err != nil {
		return err
	}
	c.peerMax = peerMax
	return nil
}

// helloErr says why no hello came: the context, a frame timeout, or what the
// reader said.
func (c *Client) helloErr(ctx context.Context, err error) error {
	//: the caller gave up first.
	if ctx.Err() != nil {
		return contextErr(ctx)
	}
	return c.readErr(err)
}

// started arms the frame's deadline once its first byte arrived.
func (c *Client) started() {
	c.keep.armRead(c.cfg.FrameTimeout)
}

// frameExpired closes a connection a frame was not whole on in time.
func (c *Client) frameExpired() {
	c.expired.Store(true)
	swallowErr(c.conn.Close())
}

// read is the connection's reader: each answer to the call it belongs to,
// until the connection ends.
func (c *Client) read() {
	for {
		f, err := readFrame(c.r, c.cfg.MaxFrameBytes, c.started)
		c.keep.disarmRead()
		//: the connection ended, or the server broke the protocol.
		if err != nil {
			c.fail(c.readErr(err))
			return
		}
		//: a frame this end cannot place ends the connection.
		if err := c.deliver(f); err != nil {
			c.fail(err)
			return
		}
	}
}

// readErr says why reading stopped.
func (c *Client) readErr(err error) error {
	switch {
	case c.expired.Load():
		return violation("a frame not whole within FrameTimeout")
	case errs.HasCode(err, corerpc.CodeFrameTooLarge), errs.HasCode(err, corerpc.CodeProtocolViolation):
		return err
	case errors.Is(err, io.ErrUnexpectedEOF):
		return violation("a frame cut short")
	default:
		return connClosedBy(err)
	}
}

// deliver hands one frame to the call it answers.
func (c *Client) deliver(f frameValue) error {
	switch f.kind {
	case kindReply, kindPong, kindItem, kindEnd, kindError:
	default:
		return violation("a frame kind a server does not send")
	}
	//: the server's word on the connection itself, before it closes it.
	if f.id == 0 {
		//: only an error speaks for the connection.
		if f.kind != kindError {
			return violation("an answer to no call")
		}
		return remoteError(f)
	}
	p, err := c.lookup(f)
	//: an id never issued, or an answer this end abandoned.
	if p == nil {
		return err
	}
	//: a call's one answer; the channel holds it, so this never waits.
	if !p.stream {
		p.answers <- f
		return nil
	}
	select {
	case p.answers <- f:
	case <-p.gone:
	}
	return nil
}

// lookup finds the call f answers. An id never issued is a violation; one
// whose caller gave up is dropped. A stream's end or error, and any call's
// answer, release its slot here.
func (c *Client) lookup(f frameValue) (*pendingCall, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.pending[f.id]
	//: abandoned, or never issued.
	if p == nil {
		//: an id this client never sent.
		if f.id > c.lastID {
			return nil, violation("an answer to a call never made")
		}
		return nil, nil
	}
	//: a call's answer, or the last frame of a stream.
	if !p.stream || f.kind != kindItem {
		delete(c.pending, f.id)
	}
	return p, nil
}

// fail ends the connection once, with err as every waiting call's error.
func (c *Client) fail(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
		c.keep.close()
		swallowErr(c.conn.Close())
	})
}

// closedErr is why the connection ended.
func (c *Client) closedErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// begin registers a call or a stream and returns its id, or refuses it before
// a byte is sent: a closed client, a method the wire cannot carry, or
// MaxInFlight already open.
func (c *Client) begin(method string, stream bool) (uint64, *pendingCall, error) {
	//: the method travels in a u8-length field.
	if len(method) > maxNameBytes {
		return 0, nil, misconfigured("a method longer than 255 bytes")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	//: the connection already ended.
	if c.err != nil {
		return 0, nil, c.err
	}
	//: never queued: refused at once.
	if len(c.pending) >= c.cfg.MaxInFlight {
		return 0, nil, errs.Wrap(corerpc.Overloaded, errs.WrapParams{},
			errs.Int("max_in_flight", c.cfg.MaxInFlight), errs.String("side", "client"))
	}
	c.lastID++
	p := &pendingCall{answers: make(chan frameValue, answersFor(stream)), stream: stream}
	//: only a stream's reader can abandon it while the connection's reader
	//: waits on it; a call's answer never waits.
	if stream {
		p.gone = make(chan struct{})
	}
	c.pending[c.lastID] = p
	return c.lastID, p, nil
}

// answersFor is how many frames a call's channel holds: one answer, or a
// stream's buffer.
func answersFor(stream bool) int {
	//: a stream's items wait for its reader.
	if stream {
		return streamBuffer
	}
	return 1
}

// forget drops a call this end no longer waits for; an answer still on its
// way is dropped by the reader.
func (c *Client) forget(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// abandon forgets a call and tells the server, best effort.
func (c *Client) abandon(id uint64) {
	c.forget(id)
	swallowErr(c.write(emptyFrame(kindCancel, id)))
}

// write sends one whole frame within FrameTimeout; a connection that cannot
// be written is over.
func (c *Client) write(frame []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.keep.armWrite(c.cfg.FrameTimeout)
	_, err := c.conn.Write(frame)
	c.keep.disarmWrite()
	//: the connection broke under the write.
	if err != nil {
		c.fail(connClosedBy(err))
		return c.closedErr()
	}
	return nil
}

// head is a call's or a stream's frame up to its budget; the request's value
// is appended to it.
func head(kind byte, id uint64, method string, budget time.Duration) []byte {
	frame := beginFrame(make([]byte, 0, frameStartBytes), kind, id, method)
	return binary.BigEndian.AppendUint64(frame, uint64(budget))
}

// send finishes a request built by head and Append and writes it; a request
// that did not encode, that the server could not read, or that the
// connection could not carry is forgotten and its error returned.
func (c *Client) send(id uint64, frame []byte, built error) error {
	//: the value did not encode.
	if built != nil {
		c.forget(id)
		return built
	}
	frame, err := finishFrame(frame, c.peerMax)
	//: longer than the server reads: nothing is written.
	if err != nil {
		c.forget(id)
		return err
	}
	//: the connection broke under the request.
	if err := c.write(frame); err != nil {
		c.forget(id)
		return err
	}
	return nil
}

// Call sends in to method and decodes the reply into out (nil drops it). It
// waits for ctx and for CallTimeout on the client's clock, whichever ends
// first: past either the server is sent a cancel frame and the call is
// CALL_TIMEOUT or CALL_CANCELLED. The server's own refusals keep their code.
func (c *Client) Call(ctx context.Context, method string, in, out any) error {
	id, p, err := c.open(ctx, method, false)
	//: refused before a byte is sent.
	if err != nil {
		return err
	}
	frame, err := c.format.Append(head(kindCall, id, method, budgetOf(ctx, c.cfg.CallTimeout)), in)
	//: not sent: in did not encode, or the connection could not carry it.
	if err := c.send(id, frame, err); err != nil {
		return err
	}
	f, err := c.await(ctx, id, p)
	//: no answer.
	if err != nil {
		return err
	}
	body, err := c.answerBody(f, kindReply)
	//: the server's error, or a caller that drops the value.
	if err != nil || out == nil {
		return err
	}
	//: the body is not a value of out's shape in this format.
	if err := c.format.Unmarshal(body, out); err != nil {
		return codecFailed("decode", err)
	}
	return nil
}

// open refuses a context already over, then registers the call.
func (c *Client) open(ctx context.Context, method string, stream bool) (uint64, *pendingCall, error) {
	//: a context already over sends nothing.
	if ctx.Err() != nil {
		return 0, nil, contextErr(ctx)
	}
	return c.begin(method, stream)
}

// Ping is a round trip that runs no handler: nil when the server answered
// within ctx and CallTimeout.
func (c *Client) Ping(ctx context.Context) error {
	id, p, err := c.open(ctx, "", false)
	//: refused before a byte is sent.
	if err != nil {
		return err
	}
	//: the connection broke under the ping.
	if err := c.write(emptyFrame(kindPing, id)); err != nil {
		c.forget(id)
		return err
	}
	f, err := c.await(ctx, id, p)
	//: no answer.
	if err != nil {
		return err
	}
	_, err = c.answerBody(f, kindPong)
	return err
}

// await waits for a call's answer, its context, CallTimeout on the clock, or
// the connection's end; giving up sends the server a cancel frame.
func (c *Client) await(ctx context.Context, id uint64, p *pendingCall) (frameValue, error) {
	t := c.clk.NewTimer(c.cfg.CallTimeout)
	defer t.Stop()
	select {
	case f := <-p.answers:
		return f, nil
	case <-ctx.Done():
		c.abandon(id)
		return frameValue{}, contextErr(ctx)
	case <-t.C():
		c.abandon(id)
		return frameValue{}, errs.Wrap(corerpc.CallTimeout, errs.WrapParams{}, errs.String("by", "CallTimeout"))
	case <-c.done:
		return frameValue{}, c.closedErr()
	}
}

// answerBody is the body of an answer of the kind expected, or the server's
// error; an answer of another kind ends the connection.
func (c *Client) answerBody(f frameValue, want byte) ([]byte, error) {
	switch f.kind {
	case want:
		return f.body, nil
	case kindError:
		return nil, remoteError(f)
	default:
		c.fail(violation("an answer of the wrong kind"))
		return nil, c.closedErr()
	}
}

// Close closes the connection; every call in flight ends with CONN_CLOSED. It
// is idempotent.
func (c *Client) Close() error {
	c.fail(connClosed())
	return nil
}

// budgetOf is what a request tells the server it has: what is left of ctx's
// deadline, at most limit when limit is positive; zero for none.
func budgetOf(ctx context.Context, limit time.Duration) time.Duration {
	budget := limit
	//: the caller's deadline is earlier than the limit, or the only one.
	if dl, ok := ctx.Deadline(); ok {
		//: the remaining real time: a context's deadline is on the wall clock.
		if left := time.Until(dl); budget <= 0 || left < budget {
			budget = max(left, 1)
		}
	}
	return budget
}

// contextErr is CALL_TIMEOUT for a context whose deadline passed and
// CALL_CANCELLED for one cancelled.
func contextErr(ctx context.Context) error {
	//: the deadline passed.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errs.Wrap(corerpc.CallTimeout, errs.WrapParams{}, errs.String("by", "the context's deadline"))
	}
	return errs.Wrap(corerpc.CallCancelled, errs.WrapParams{}, errs.String("by", "the caller"))
}
