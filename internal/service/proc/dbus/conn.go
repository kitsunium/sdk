package dbus

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coredbus "github.com/kitsunium/sdk/internal/core/proc/dbus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// busDriver is the bus driver's name: every call of this client goes to
	// it, and the signals it reads come from it.
	busDriver string = "org.freedesktop.DBus"
	// busPath is the bus driver's object path.
	busPath string = "/org/freedesktop/DBus"
	// busInterface is the bus driver's interface.
	busInterface string = "org.freedesktop.DBus"
	// nameHasNoOwner is GetNameOwner's error for a name nobody owns.
	nameHasNoOwner string = "org.freedesktop.DBus.Error.NameHasNoOwner"
	// requestDoNotQueue is RequestName's DBUS_NAME_FLAG_DO_NOT_QUEUE.
	requestDoNotQueue uint32 = 4
	// replyPrimaryOwner is RequestName's answer for a name just given.
	replyPrimaryOwner uint32 = 1
	// replyAlreadyOwner is RequestName's answer for a name the connection
	// owned already.
	replyAlreadyOwner uint32 = 4
	// releaseReleased is ReleaseName's answer for a name given back.
	releaseReleased uint32 = 1
	// readBuffer is the connection's read buffer.
	readBuffer int = 4096
	// exitUnavailable is EX_UNAVAILABLE.
	exitUnavailable int = 69
	// exitTempFail is EX_TEMPFAIL.
	exitTempFail int = 75
	// osLinux is the GOOS the client runs on.
	osLinux string = "linux"
	// callTimeout bounds every call and the authentication: sd-bus's and
	// libdbus's default reply timeout.
	callTimeout time.Duration = 25 * time.Second
)

// Conn is one connection to a message bus, safe for concurrent use: its calls
// and watches share the connection, each call answered by its serial. It is a
// proc/service BusWatcher as it is. Close it once done.
type Conn struct {
	nc net.Conn
	r  *bufio.Reader
	// done closes once the connection ended.
	done chan struct{}
	// pending are the calls waiting for a reply, by serial; watches the
	// names watched. Guarded by mu.
	pending map[uint32]*pending
	watches map[string][]*watch
	// cause is why the connection ended. Guarded by mu.
	cause error
	// uniqueName is the connection's name on the bus, Hello's answer.
	uniqueName string
	// wg joins the reader and every watch's goroutine.
	wg sync.WaitGroup
	// doneOnce closes done.
	doneOnce sync.Once
	mu       sync.Mutex
	// wmu writes one message at a time.
	wmu    sync.Mutex
	serial atomic.Uint32
	// closed refuses every new call and watch. Guarded by mu.
	closed bool
}

// pending is one call waiting for its reply; onReply, when set, runs in the
// reader before the next message is read.
type pending struct {
	reply   chan *message
	onReply func(*message)
}

// NewConn connects to the bus at address, authenticates with AUTH EXTERNAL
// and says Hello, within ctx — and within 25 seconds a step, CALL_TIMEOUT
// past them. An address with no unix socket entry is ADDRESS_INVALID; a socket
// that cannot be connected BUS_UNREACHABLE; a refused authentication
// AUTH_REFUSED; a reply the protocol forbids PROTOCOL_VIOLATION. It is
// UNSUPPORTED_PLATFORM off Linux.
//
// Goroutine lifecycle: the connection runs one reader, ended by Close or by
// the bus hanging up, and one goroutine per watch, ended with its watch.
func NewConn(ctx context.Context, address string) (*Conn, error) {
	//: the credentials EXTERNAL rests on are Linux's (SO_PEERCRED).
	if runtime.GOOS != osLinux {
		//: UNSUPPORTED_PLATFORM.
		return nil, errs.Wrap(coreproc.UnsupportedPlatform, errs.WrapParams{}, errs.String("goos", runtime.GOOS))
	}
	socket, err := socketOf(address)
	//: no socket to dial.
	if err != nil {
		//: ADDRESS_INVALID.
		return nil, err
	}
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unix", socket)
	//: nobody listens there.
	if err != nil {
		//: BUS_UNREACHABLE.
		return nil, unreachable(err)
	}
	//: authenticated and named.
	return open(ctx, nc)
}

// open authenticates over nc, starts the reader and says Hello; a
// failure closes nc.
//
// Goroutine lifecycle: the reader, ended when nc is — Close, the bus's
// hang-up, a message the client cannot read —; joined by Close.
func open(ctx context.Context, nc net.Conn) (*Conn, error) {
	r := bufio.NewReaderSize(nc, readBuffer)
	//: AUTH EXTERNAL, within ctx.
	if err := handshake(ctx, nc, r); err != nil {
		gone(nc.Close())
		//: refused, cancelled, or the bus hung up.
		return nil, err
	}
	c := &Conn{nc: nc, r: r, done: make(chan struct{}), pending: map[uint32]*pending{}, watches: map[string][]*watch{}}
	c.wg.Add(1)
	//: Goroutine lifecycle: see above.
	go c.read()
	m, err := c.call(ctx, &callValue{member: "Hello"})
	//: no unique name.
	if err != nil {
		gone(c.Close())
		//: the call's failure.
		return nil, err
	}
	names, ok := m.strings(1)
	//: a reply that names none.
	if !ok || names[0] == "" {
		gone(c.Close())
		//: PROTOCOL_VIOLATION.
		return nil, violation("Hello reply")
	}
	c.uniqueName = names[0]
	//: connected.
	return c, nil
}

// handshake runs the authentication within ctx and callTimeout: the sooner
// deadline, and ctx's cancellation through an expired one, end a read or a
// write in flight.
func handshake(ctx context.Context, nc net.Conn, r io.ByteReader) error {
	bound := time.Now().Add(callTimeout)
	deadline, byCtx := ctx.Deadline()
	//: no deadline, or a later one than the bound.
	if !byCtx || deadline.After(bound) {
		deadline, byCtx = bound, false
	}
	gone(nc.SetDeadline(deadline))
	stop := context.AfterFunc(ctx, func() { gone(nc.SetDeadline(time.Unix(1, 0))) })
	err := authenticate(nc, r)
	cut := !stop()
	gone(nc.SetDeadline(time.Time{}))
	//: ctx ended during the authentication.
	if cut || (err != nil && ctx.Err() != nil) {
		//: CALL_CANCELLED or CALL_TIMEOUT.
		return ended("AUTH", ctx.Err())
	}
	//: the deadline passed — ctx's, though it may not say so yet, or the bound.
	if err != nil && isTimeout(err) {
		//: CALL_TIMEOUT, ctx's deadline in the chain when it was the one.
		return timedOut("AUTH", deadlineCause(byCtx))
	}
	//: authenticated, or refused.
	return err
}

// deadlineCause is context.DeadlineExceeded when the deadline that passed was
// the context's, nil when it was the client's own bound.
func deadlineCause(byCtx bool) error {
	//: the context's.
	if byCtx {
		//: as the context will say.
		return context.DeadlineExceeded
	}
	//: the bound's.
	return nil
}

// isTimeout reports a read or a write that passed its deadline.
func isTimeout(err error) bool {
	ne, ok := errors.AsType[net.Error](err)
	//: the network's own timeout.
	return ok && ne.Timeout()
}

// UniqueName is the connection's unique name on the bus (":1.42").
func (c *Conn) UniqueName() string {
	//: as Hello named it.
	return c.uniqueName
}

// NameOwner returns the unique name of name's owner, empty when it has none.
// A name that is not well-known is NAME_INVALID.
func (c *Conn) NameOwner(ctx context.Context, name string) (string, error) {
	//: a well-known name.
	if err := checkName(name); err != nil {
		//: NAME_INVALID.
		return "", err
	}
	m, err := c.roundTrip(ctx, &callValue{member: "GetNameOwner", sig: "s", strs: []string{name}}, nil)
	//: no reply.
	if err != nil {
		//: cancelled, or the connection is gone.
		return "", err
	}
	//: the owner, or none.
	return ownerOf(m)
}

// RequestName asks the bus for name, never queued: true when the
// connection owns it now — just given, or already its own —, false when
// another owns it. A policy that forbids it is CALL_FAILED.
func (c *Conn) RequestName(ctx context.Context, name string) (bool, error) {
	//: a well-known name.
	if err := checkName(name); err != nil {
		//: NAME_INVALID.
		return false, err
	}
	reply, err := c.answer(ctx, &callValue{member: "RequestName", sig: "su", strs: []string{name}, u: requestDoNotQueue})
	//: owned now, or not.
	return reply == replyPrimaryOwner || reply == replyAlreadyOwner, err
}

// ReleaseName gives name back: true when the connection owned it, false when
// it did not.
func (c *Conn) ReleaseName(ctx context.Context, name string) (bool, error) {
	//: a well-known name.
	if err := checkName(name); err != nil {
		//: NAME_INVALID.
		return false, err
	}
	reply, err := c.answer(ctx, &callValue{member: "ReleaseName", sig: "s", strs: []string{name}})
	//: released, or not this connection's.
	return reply == releaseReleased, err
}

// Close closes the connection: every call in flight ends with CONN_CLOSED,
// every watch's channel is closed, and the names the connection owned are
// released by the bus. It is idempotent.
func (c *Conn) Close() error {
	c.fail(nil)
	c.wg.Wait()
	//: closed.
	return nil
}

// answer sends a call whose reply is one uint32, and returns it.
func (c *Conn) answer(ctx context.Context, cv *callValue) (uint32, error) {
	m, err := c.call(ctx, cv)
	//: refused or lost.
	if err != nil {
		//: the call's failure.
		return 0, err
	}
	v, ok := m.uint32()
	//: a reply of another shape.
	if !ok {
		//: PROTOCOL_VIOLATION.
		return 0, violation(cv.member + " reply")
	}
	//: the answer.
	return v, nil
}

// call sends cv and returns its method return; an error message is
// CALL_FAILED naming it.
func (c *Conn) call(ctx context.Context, cv *callValue) (*message, error) {
	m, err := c.roundTrip(ctx, cv, nil)
	//: no reply.
	if err != nil {
		//: cancelled, or the connection is gone.
		return nil, err
	}
	//: the bus refused it.
	if m.typ == typeError {
		//: CALL_FAILED.
		return nil, callFailed(cv.member, m.errName)
	}
	//: the reply.
	return m, nil
}

// roundTrip sends cv and returns its reply, a method return or an error
// message; onReply runs in the reader as the reply is read.
func (c *Conn) roundTrip(ctx context.Context, cv *callValue, onReply func(*message)) (*message, error) {
	serial := c.nextSerial()
	p := &pending{reply: make(chan *message, 1), onReply: onReply}
	c.mu.Lock()
	//: a closed connection takes no call.
	if c.closed {
		c.mu.Unlock()
		//: CONN_CLOSED.
		return nil, coredbus.ConnClosed
	}
	c.pending[serial] = p
	c.mu.Unlock()
	//: written, or the connection is gone.
	if err := c.send(ctx, serial, cv); err != nil {
		c.forget(serial)
		//: CONN_CLOSED.
		return nil, err
	}
	bound := time.NewTimer(callTimeout)
	defer bound.Stop()
	//: the reply, the caller's end, the bound, or the connection's end.
	select {
	//: answered.
	case m := <-p.reply:
		//: the reply.
		return m, nil
	//: the caller gave up: a late reply is dropped.
	case <-ctx.Done():
		c.forget(serial)
		//: CALL_CANCELLED or CALL_TIMEOUT.
		return nil, ended(cv.member, ctx.Err())
	//: the bus did not answer in time: a late reply is dropped.
	case <-bound.C:
		c.forget(serial)
		//: CALL_TIMEOUT.
		return nil, timedOut(cv.member, nil)
	//: the connection ended.
	case <-c.done:
		//: a reply read before the end.
		return c.lateReply(p)
	}
}

// lateReply is a reply read before the connection ended, or CONN_CLOSED.
func (c *Conn) lateReply(p *pending) (*message, error) {
	//: read already.
	select {
	//: the reply.
	case m := <-p.reply:
		//: answered.
		return m, nil
	//: none.
	default:
		//: CONN_CLOSED.
		return nil, coredbus.ConnClosed
	}
}

// nextSerial is the next serial, never 0.
func (c *Conn) nextSerial() uint32 {
	s := c.serial.Add(1)
	//: wrapped round.
	if s == 0 {
		s = c.serial.Add(1)
	}
	//: the serial.
	return s
}

// forget drops a call nobody waits for.
func (c *Conn) forget(serial uint32) {
	c.mu.Lock()
	delete(c.pending, serial)
	c.mu.Unlock()
}

// send writes one call, within ctx's deadline. A message half written leaves
// the stream unreadable: a failed write ends the connection.
func (c *Conn) send(ctx context.Context, serial uint32, cv *callValue) error {
	b := encodeCall(serial, cv)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	deadline, _ := ctx.Deadline()
	gone(c.nc.SetWriteDeadline(deadline))
	//: written.
	if _, err := c.nc.Write(b); err != nil {
		c.fail(err)
		//: CONN_CLOSED.
		return coredbus.ConnClosed
	}
	//: sent.
	return nil
}

// fail ends the connection for cause (nil for Close): no new call or watch,
// the socket closed, which ends the reader.
func (c *Conn) fail(cause error) {
	c.mu.Lock()
	//: the first cause is the one kept.
	if c.cause == nil {
		c.cause = cause
	}
	c.closed = true
	c.mu.Unlock()
	gone(c.nc.Close())
}

// read reads every message until the connection ends, and ends it.
func (c *Conn) read() {
	defer c.wg.Done()
	var err error
	//: until the connection ends, or a message breaks the protocol.
	for err == nil {
		var m *message
		m, err = readMessage(c.r)
		//: a message to hand.
		if err == nil {
			c.dispatch(m)
		}
	}
	c.end(err)
}

// end closes the connection after its reader stopped: every call waiting and
// every watch learn it through done.
func (c *Conn) end(cause error) {
	c.fail(cause)
	c.mu.Lock()
	c.pending, c.watches = nil, nil
	c.mu.Unlock()
	c.doneOnce.Do(func() { close(c.done) })
}

// dispatch hands one message: a reply to its call, NameOwnerChanged to its
// watches; a method call another peer sends is dropped, as is a type the
// specification lets a reader ignore.
func (c *Conn) dispatch(m *message) {
	//: by the type.
	switch m.typ {
	//: a reply.
	case typeMethodReturn, typeError:
		c.reply(m)
	//: a signal.
	case typeSignal:
		c.announced(m)
	//: a call to this connection, or an unknown type.
	default:
	}
}

// reply hands the bus driver's reply to the call it answers.
func (c *Conn) reply(m *message) {
	//: every call goes to the bus driver.
	if m.sender != busDriver {
		return
	}
	c.mu.Lock()
	p := c.pending[m.replySerial]
	delete(c.pending, m.replySerial)
	c.mu.Unlock()
	//: a call nobody waits for any more.
	if p == nil {
		return
	}
	//: what must happen before the next message is read.
	if p.onReply != nil {
		p.onReply(m)
	}
	p.reply <- m
}

// announced hands the bus driver's NameOwnerChanged to the name's watches
// that looked their owner up already: an announcement read before the lookup
// is older than it.
func (c *Conn) announced(m *message) {
	//: the bus driver's NameOwnerChanged alone.
	if m.sender != busDriver || m.path != busPath || m.iface != busInterface || m.member != "NameOwnerChanged" {
		return
	}
	args, ok := m.strings(3)
	//: a body of another shape.
	if !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	//: each watch of the name.
	for _, w := range c.watches[args[0]] {
		//: newer than its lookup.
		if w.ready {
			w.push(args[2])
		}
	}
}

// ownerOf reads GetNameOwner's reply: the owner, empty for NameHasNoOwner.
func ownerOf(m *message) (string, error) {
	//: an error message.
	if m.typ == typeError {
		//: nobody owns the name.
		if m.errName == nameHasNoOwner {
			//: no owner.
			return "", nil
		}
		//: CALL_FAILED.
		return "", callFailed("GetNameOwner", m.errName)
	}
	owner, ok := m.strings(1)
	//: a reply of another shape.
	if !ok {
		//: PROTOCOL_VIOLATION.
		return "", violation("GetNameOwner reply")
	}
	//: the owner.
	return owner[0], nil
}

// callFailed is CALL_FAILED naming the method and the bus's error.
func callFailed(method, errName string) error {
	//: the core sentinel with the names.
	return errs.Wrap(coredbus.CallFailed, errs.WrapParams{}, errs.String("method", method), errs.String("error", errName))
}

// ended is a call whose context ended: CALL_TIMEOUT for its deadline,
// CALL_CANCELLED otherwise, ctx's error in the chain.
func ended(method string, cause error) error {
	//: the deadline passed.
	if errors.Is(cause, context.DeadlineExceeded) {
		//: CALL_TIMEOUT.
		return timedOut(method, cause)
	}
	//: origin wins on a typed cause.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    coredbus.CodeCallCancelled,
		Reason:  "CALL_CANCELLED",
		Public:  "The message bus call was cancelled",
		Private: "service/proc/dbus: the caller's context was cancelled before the reply came; the method field says which call",
	}, errs.String("method", method))
}

// timedOut is CALL_TIMEOUT naming the method, cause — a deadline that
// passed — in the chain when there is one.
func timedOut(method string, cause error) error {
	//: the bound alone: the core sentinel.
	if cause == nil {
		//: CALL_TIMEOUT.
		return errs.Wrap(coredbus.CallTimeout, errs.WrapParams{}, errs.String("method", method))
	}
	//: origin wins on a typed cause.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coredbus.CodeCallTimeout,
		Reason:   "CALL_TIMEOUT",
		Public:   "The message bus did not answer in time",
		Private:  "service/proc/dbus: no reply within the context's deadline or 25 seconds, the default reply timeout; the method field says which call",
		ExitCode: exitTempFail,
	}, errs.String("method", method))
}

// gone takes an error nobody can act on: a deadline on a connection that is
// closing, a socket closed whatever its close says.
func gone(err error) {
	//: read, so the discard is deliberate.
	if err == nil {
		return
	}
	//: nothing left to do.
}
