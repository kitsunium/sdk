// Package websocket is the server side of RFC 6455: an HTTP request upgraded
// into a bidirectional, message-oriented connection over the same socket.
//
// It is written against net/http's own interfaces, not against this SDK's
// listener engine, so it works inside any http.Handler. Mounted on the SDK
// engine it additionally observes the server's drain signal, which is what
// stops a connection that by design never ends from being severed under its
// handler on every deployment.
//
// permessage-deflate and every other extension are deliberately NOT negotiated;
// the reserved frame bits an extension would use are refused as protocol
// errors. See the package CLAUDE.md.
package websocket

import (
	"bufio"
	"io"
	stdnet "net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/worker"
)

// initialWriteCapacity pre-sizes the per-connection frame buffer. A few hundred
// bytes covers the overwhelming majority of messages, and the buffer is reused
// for the connection's whole life, so a steady-state send allocates nothing.
const initialWriteCapacity int = 512

// Conn is one upgraded WebSocket connection.
//
// Every connection needs exactly ONE goroutine looping on [Conn.Receive] for
// its whole life — not two, and not zero — and that includes a connection the
// handler only ever writes to.
//
// Not two, because the protocol is a single ordered frame stream: two
// concurrent readers would each take half of a message.
//
// Not zero, because that loop is more than the way messages arrive. It is
// where the peer's Ping is answered (RFC 6455 §5.5.2), where its Close is
// replied to (§5.5.1), and the only place the heartbeat can see that the peer
// is alive: the heartbeat counts frames the handler has READ, so a Pong the
// peer sent on time but nobody read is, to it, silence. A handler that only
// pushes therefore runs the loop in a goroutine of its own and discards what
// it reads. Without one, the heartbeat ends the connection within two ping
// intervals — about a minute at [DefaultPingInterval] — however healthy the
// peer is. Nothing here starts that reader for you: it would have to own the
// buffer Receive hands out, and owning it is what makes a steady-state read
// free.
//
// Writes are safe for concurrent use — the heartbeat writes from its own
// goroutine while the handler writes from another, and a handler fanning
// messages in from several producers is the normal shape.
type Conn struct {
	// raw is the socket taken over from net/http at the upgrade. It is ours
	// from that moment: the engine no longer closes it, and neither does
	// net/http.
	raw stdnet.Conn
	// br is the reader the hijack handed back. It MUST be used rather than the
	// socket: net/http may have buffered bytes into it while parsing the
	// request, and reading past it would silently drop them.
	br *bufio.Reader
	// subprotocol is what the handshake agreed on, or "" when nothing was.
	subprotocol string
	// cfg is the resolved option set.
	cfg config

	// wmu serialises whole frames. Every write must be whole: two interleaved
	// frames are not two messages, they are one corrupt stream.
	wmu sync.Mutex
	// wbuf is the reusable encode buffer, guarded by wmu.
	wbuf []byte
	// closeSent records that this endpoint has already put a Close frame on the
	// wire. RFC 6455 §5.5.1 allows exactly one, and a second would be read as a
	// frame arriving after the closing handshake finished. Guarded by wmu.
	closeSent bool

	// msg accumulates the message being reassembled. Reader goroutine only.
	msg []byte
	// assembling records that a fragmented message is in progress, which is
	// what makes a continuation frame legal and a new data frame illegal.
	assembling bool
	// msgOp is the opcode of the message in progress — the first frame's, since
	// a continuation frame does not repeat it.
	msgOp corenet.WSOpCode
	// hdr is the fixed scratch for one frame header. Reader goroutine only.
	hdr [corenet.WSMaxHeaderLen]byte
	// ctl is the fixed scratch for one control-frame payload. It is a separate
	// buffer from msg on purpose: a control frame may arrive BETWEEN two
	// fragments, so answering it must not disturb the message being assembled.
	ctl [corenet.WSMaxControlPayload]byte

	// rx counts frames Receive has READ, which is not the same as frames that
	// have arrived: one still waiting in the socket buffer has not moved it.
	// The heartbeat reads it to tell a live peer from a silent one; the value
	// is meaningless, only its movement matters.
	rx atomic.Uint64
	// peerCode is the close code the peer sent, or zero when it sent none.
	peerCode atomic.Uint32

	// done is closed exactly once, when the connection ends for any reason.
	done chan struct{}
	// endOnce guards close(done) against the several goroutines that can end a
	// connection: the handler, the heartbeat, and the drain watcher.
	endOnce sync.Once
	// watcher owns the goroutine translating the server's drain signal into
	// a closing handshake.
	watcher *worker.LoopDaemon
	// pinger owns the heartbeat goroutine; nil when the heartbeat is disabled.
	pinger *worker.LoopDaemon
}

// NewConn assembles the connection and starts the goroutines it owns.
//
// It is exported only because the struct-constructor lint requires a
// New-prefixed constructor beside every exported struct; it takes an unexported
// option set, so nothing outside this package can call it. [Upgrade] is the
// real entry point, and the public façade re-exports only that.
func NewConn(socket stdnet.Conn, buffered *bufio.Reader, subprotocol string, cfg *config, draining <-chan struct{}) *Conn {
	c := &Conn{
		raw:         socket,
		br:          buffered,
		subprotocol: subprotocol,
		cfg:         *cfg,
		wbuf:        make([]byte, 0, initialWriteCapacity),
		done:        make(chan struct{}),
	}
	//: net/http may have installed the group's ReadTimeout / WriteTimeout on
	//: this socket. Those are per-REQUEST bounds, and there is no request any
	//: more: left in place they would cut the connection at a fixed instant,
	//: silently, however healthy it was — and the operator would see a fleet of
	//: clients reconnecting on a cycle nobody configured.
	swallowErr(socket.SetDeadline(time.Time{}))
	//: the drain signal is captured as a CHANNEL, not read from the context
	//: later: net/http cancels the request context when the handler returns,
	//: and a hijacked socket outlives the handler by design.
	c.watch(draining)
	c.startHeartbeat()
	//: open, watched and probed.
	return c
}

// Subprotocol returns the subprotocol the handshake agreed on, or "" when none
// was.
func (c *Conn) Subprotocol() string {
	//: fixed at the handshake; nothing can renegotiate it afterwards.
	return c.subprotocol
}

// Done returns the channel closed when the connection has ended: the peer sent
// Close, the socket died, the heartbeat found the peer silent, the server began
// draining, or the handler closed it.
//
// Done is for the goroutines that are NOT reading. The one goroutine every
// connection needs looping on Receive (see [Conn]) learns of the end from
// Receive's terminal error at the same moment. A handler that pushes
// application events selects on Done beside its event source, so it stops
// between events rather than on the next failed Send.
//
// Watching Done does not replace the reader. A connection nobody reads answers
// no Ping, replies to no Close, and is ended by the heartbeat within two ping
// intervals, however healthy the peer is.
func (c *Conn) Done() <-chan struct{} {
	//: read-only, so nobody but the connection can end it.
	return c.done
}

// PeerCloseCode returns the status code the peer's Close frame carried.
//
// It reports [corenet.WSCloseNoStatus] when the peer closed without one — the
// code that exists to describe exactly that — and zero when no Close frame was
// ever received, which is the case that matters most: it distinguishes a peer
// that said goodbye from one that vanished.
func (c *Conn) PeerCloseCode() corenet.WSCloseCode {
	//: stored by the reader when a Close frame is parsed.
	return corenet.WSCloseCode(c.peerCode.Load())
}

// Receive reads the next complete message, answering control frames on the way.
//
// It is called in a loop, by one goroutine, for the connection's whole life —
// on a connection with nothing to read as much as on any other. Control
// frames are served INSIDE this call: the Pong a Ping is owed, the Close a
// Close is owed, and the count the heartbeat reads to decide the peer is
// alive. Stop calling it and all three stop with it; see [Conn].
//
// It returns a message only when one is whole: fragmentation is the sender's
// private choice of chunk size, not a semantic boundary, so surfacing it would
// put an implementation detail of the peer's writer into every consumer.
//
// The returned Data aliases the connection's own reassembly buffer and is valid
// until the next call to Receive. A handler that keeps a message past that must
// copy it — the same rule the engine's Conn.Buffer() carries, for the same
// reason: reusing one buffer is what makes a steady-state read allocate
// nothing.
func (c *Conn) Receive() (message corenet.WSMessageValue, err error) {
	//: the terminal check comes first, so a handler looping on Receive sees the
	//: same reason to stop whatever the connection was doing.
	if eerr := c.ended(); eerr != nil {
		//: the connection is over.
		return corenet.WSMessageValue{}, eerr
	}
	//: control frames do not complete a message, so the loop continues past
	//: them — which is exactly what "a control frame may be injected between
	//: two fragments" means in code.
	for {
		header, herr := c.nextHeader()
		//: a framing error has already failed the connection.
		if herr != nil {
			//: hand the reason to the handler.
			return corenet.WSMessageValue{}, herr
		}
		c.rx.Add(1)
		//: Ping, Pong and Close are answered here and never surface as
		//: messages: they are transport, not payload.
		if header.OpCode.IsControl() {
			//: a control frame either continues the loop or ends the
			//: connection; it can never complete a message.
			if cerr := c.serveControl(header); cerr != nil {
				//: the peer closed, or the frame was malformed.
				return corenet.WSMessageValue{}, cerr
			}
			continue
		}
		complete, derr := c.assemble(header)
		//: the connection has already been failed.
		if derr != nil {
			//: hand the reason to the handler.
			return corenet.WSMessageValue{}, derr
		}
		//: more fragments to come.
		if !complete {
			continue
		}
		//: the message is whole, which is the only point at which UTF-8 can
		//: honestly be judged.
		return c.completeMessage()
	}
}

// assemble applies the fragmentation rules to one data frame and appends its
// payload to the message in progress, reporting whether that message is now
// whole.
func (c *Conn) assemble(header corenet.WSFrameHeaderValue) (complete bool, err error) {
	//: the fragmentation rules are checked before the payload is read, so a
	//: frame that must not exist never grows the buffer.
	if ferr := c.trackFragment(header); ferr != nil {
		//: the connection has already been failed.
		return false, ferr
	}
	//: the payload is appended to the message being assembled.
	if derr := c.readDataPayload(header); derr != nil {
		//: the connection has already been failed.
		return false, derr
	}
	//: FIN is what says the message is finished, not the buffer's size.
	return header.Final, nil
}

// Send writes one message as a single frame and returns once it is on the wire.
//
// The SDK never fragments what it sends. Fragmentation exists so a sender can
// begin a message whose length it does not yet know; every message this API can
// express is already in memory, so splitting it would add a failure mode
// (a half-sent message when the socket dies mid-sequence) in exchange for
// nothing.
func (c *Conn) Send(message corenet.WSMessageValue) error {
	//: a text frame must be valid UTF-8 on the way out as well. Emitting what
	//: this endpoint would refuse to receive is how two implementations of one
	//: RFC drift apart — and the peer is required to fail the connection over
	//: it, so the caller would see a mysterious disconnect instead of an error.
	if !message.Binary {
		//: refuse before anything reaches the socket.
		if verr := corenet.ValidateWSText(message.Data); verr != nil {
			//: the error already names what was wrong with it.
			return verr
		}
	}
	//: one whole frame, under the write lock.
	return c.sendFrame(message.OpCode(), message.Data)
}

// SendText writes one UTF-8 text message.
func (c *Conn) SendText(text string) error {
	//: the conversion is what the wire needs; validation happens in Send.
	return c.Send(corenet.WSMessageValue{Data: []byte(text)})
}

// SendBinary writes one binary message.
func (c *Conn) SendBinary(data []byte) error {
	//: opaque bytes, so there is nothing to validate.
	return c.Send(corenet.WSMessageValue{Binary: true, Data: data})
}

// Ping sends a Ping frame carrying payload.
//
// It is exported because the automatic heartbeat answers "is the peer alive on
// its own schedule"; a caller sometimes needs to ask at a moment of its own —
// right after a long computation, or before committing to expensive work on the
// peer's behalf. A payload past [corenet.WSMaxControlPayload] is refused: a
// control frame must fit a buffer that is always available.
func (c *Conn) Ping(payload []byte) error {
	//: the control-frame ceiling is enforced by the frame encoder.
	return c.sendFrame(corenet.WSPing, payload)
}

// Close ends the connection with a normal-closure status.
//
// A handler should defer it. It returns nil even when the socket has already
// gone: by the time a handler is closing, the peer having left first is the
// expected ending, not a fault to report. Use [Conn.CloseWith] to say something
// more specific.
func (c *Conn) Close() error {
	//: the status code every clean ending carries.
	return c.CloseWith(corenet.WSCloseNormal, "")
}

// CloseWith ends the connection with a specific status code and reason.
//
// It reports an error only when the CODE or the reason is unusable — a code the
// RFC forbids on the wire, a reason that is not UTF-8 or does not fit a control
// frame. Those are caller mistakes and must surface whatever the socket is
// doing; a transport failure at this point is not, because there is nothing
// left to do about it.
func (c *Conn) CloseWith(code corenet.WSCloseCode, reason string) error {
	var scratch [corenet.WSMaxControlPayload]byte
	//: validated BEFORE anything is torn down, so a caller bug is reported even
	//: when the peer had already closed — otherwise the bug would hide behind
	//: whichever end happened to finish first.
	if _, verr := corenet.AppendWSClosePayload(scratch[:0], code, reason); verr != nil {
		c.terminate()
		c.join()
		//: the error already names what was wrong with the code or the reason.
		return verr
	}
	//: a closing handshake the peer may never see is still worth attempting;
	//: it is what turns an abrupt disconnect into a stated ending.
	swallowErr(c.sendClose(code, reason))
	c.terminate()
	//: joining is safe here because Close is the handler's call; the heartbeat
	//: and the watcher end the connection through terminate, never through
	//: Close, so neither can ever be joining itself.
	c.join()
	//: closed.
	return nil
}

// nextHeader reads and validates one frame header.
func (c *Conn) nextHeader() (header corenet.WSFrameHeaderValue, err error) {
	//: exactly two bytes, because two bytes is what announces how many more the
	//: header needs. Reading speculatively would consume payload this endpoint
	//: has not yet decided it will accept.
	if _, rerr := io.ReadFull(c.br, c.hdr[:corenet.WSMinHeaderLen]); rerr != nil {
		//: the socket is gone, or the peer stopped mid-header.
		return corenet.WSFrameHeaderValue{}, c.socketGone(rerr)
	}
	want := corenet.WSFrameHeaderLen(c.hdr[:corenet.WSMinHeaderLen])
	//: the extended length and the mask key, when this frame carries them.
	if want > corenet.WSMinHeaderLen {
		//: the socket is gone, or the peer stopped mid-header.
		if _, rerr := io.ReadFull(c.br, c.hdr[corenet.WSMinHeaderLen:want]); rerr != nil {
			//: report the terminal state.
			return corenet.WSFrameHeaderValue{}, c.socketGone(rerr)
		}
	}
	parsed, perr := corenet.ParseWSFrameHeader(c.hdr[:want])
	//: reserved bits, reserved opcodes, control-frame shape, minimal length.
	if perr != nil {
		//: fail the connection — §7.1.7.
		return corenet.WSFrameHeaderValue{}, c.failConnection(corenet.WSCloseProtocolError, perr)
	}
	//: §5.1 — a server that accepted an unmasked client frame would hand a
	//: hostile script the one mechanism the design has against a transparent
	//: proxy reading the payload as a second HTTP request.
	if merr := parsed.ValidateFromClient(); merr != nil {
		//: fail the connection.
		return corenet.WSFrameHeaderValue{}, c.failConnection(corenet.WSCloseProtocolError, merr)
	}
	//: the ceiling is checked against the ANNOUNCED length, before a single
	//: byte is read or allocated. A control frame is exempt because the RFC
	//: already caps it at 125 and a caller's smaller ceiling would otherwise
	//: make Ping unanswerable.
	if !parsed.OpCode.IsControl() && parsed.Length > uint64(c.cfg.maxFrameSize) {
		//: refuse before allocating from a number the peer chose.
		return corenet.WSFrameHeaderValue{}, c.failConnection(corenet.WSCloseTooLarge,
			errs.Wrap(corenet.WSMessageTooLarge, errs.WrapParams{},
				errs.Int64("announced", int64(parsed.Length)),
				errs.Int64("ceiling", c.cfg.maxFrameSize),
				errs.String("why", "the frame announces more than the frame ceiling")))
	}
	//: a header this endpoint is willing to act on.
	return parsed, nil
}

// trackFragment applies RFC 6455 §5.4's ordering rules to a data frame.
func (c *Conn) trackFragment(header corenet.WSFrameHeaderValue) error {
	//: a continuation continues something, so there must be something.
	if header.OpCode == corenet.WSContinuation {
		//: an orphan continuation means the two endpoints disagree about what
		//: has been sent, which no amount of reading will resolve.
		if !c.assembling {
			//: fail the connection.
			return c.failConnection(corenet.WSCloseProtocolError,
				errs.Wrap(corenet.WSProtocolViolation, errs.WrapParams{},
					errs.String("why", "a continuation frame arrived with no message in progress")))
		}
		//: legal continuation.
		return nil
	}
	//: a second data frame while a message is open would interleave two
	//: messages on a stream that has no way to tell them apart — §5.4 forbids
	//: it precisely because the receiver could not reassemble either.
	if c.assembling {
		//: fail the connection.
		return c.failConnection(corenet.WSCloseProtocolError,
			errs.Wrap(corenet.WSProtocolViolation, errs.WrapParams{},
				errs.String("opcode", header.OpCode.String()),
				errs.String("why", "a new data frame interrupted a fragmented message")))
	}
	c.assembling = true
	//: the opcode is carried by the FIRST frame only; every continuation
	//: inherits it, so it has to be remembered here.
	c.msgOp = header.OpCode
	c.msg = c.msg[:0]
	//: a new message has begun.
	return nil
}

// readDataPayload appends one data frame's payload to the message in progress.
func (c *Conn) readDataPayload(header corenet.WSFrameHeaderValue) error {
	start := len(c.msg)
	//: the accumulated total is what the ceiling is about: a peer that cannot
	//: exceed it in one frame can still exceed it in a thousand, which is what
	//: fragmentation is for.
	if int64(start)+int64(header.Length) > c.cfg.maxMessageSize {
		//: refuse before growing the buffer.
		return c.failConnection(corenet.WSCloseTooLarge,
			errs.Wrap(corenet.WSMessageTooLarge, errs.WrapParams{},
				errs.Int64("accumulated", int64(start)+int64(header.Length)),
				errs.Int64("ceiling", c.cfg.maxMessageSize),
				errs.String("why", "the reassembled message exceeds the message ceiling")))
	}
	size := int(header.Length)
	//: grown from a length that has already been bounded twice, never from the
	//: raw 64-bit field.
	c.msg = slices.Grow(c.msg, size)[:start+size]
	//: the socket is gone, or the peer announced more than it sent.
	if _, rerr := io.ReadFull(c.br, c.msg[start:]); rerr != nil {
		//: report the terminal state.
		return c.socketGone(rerr)
	}
	//: unmasked in place: the payload already sits in the buffer the handler
	//: will be handed, so copying it out to unmask would double the cost of
	//: every frame for nothing.
	corenet.ApplyWSMask(c.msg[start:], header.MaskKey)
	//: appended.
	return nil
}

// completeMessage finishes the reassembled message and validates it.
func (c *Conn) completeMessage() (message corenet.WSMessageValue, err error) {
	c.assembling = false
	//: UTF-8 is judged on the WHOLE message, never per frame: a multi-byte
	//: sequence may straddle a fragment boundary, so a per-frame check would
	//: reject valid messages whose only fault is where the sender split them.
	if c.msgOp == corenet.WSText {
		//: §8.1 — invalid UTF-8 fails the connection with 1007.
		if verr := corenet.ValidateWSText(c.msg); verr != nil {
			//: fail the connection.
			return corenet.WSMessageValue{}, c.failConnection(corenet.WSCloseInvalidPayload, verr)
		}
	}
	//: the payload aliases the reassembly buffer — see Receive's doc comment.
	return corenet.WSMessageValue{Binary: c.msgOp == corenet.WSBinary, Data: c.msg}, nil
}

// serveControl answers one control frame.
func (c *Conn) serveControl(header corenet.WSFrameHeaderValue) error {
	size := int(header.Length)
	payload := c.ctl[:size]
	//: a zero-length control frame is legal and common — an empty Ping is the
	//: cheapest heartbeat there is.
	if size > 0 {
		//: the socket is gone, or the peer announced more than it sent.
		if rerr := c.readControlPayload(payload, header.MaskKey); rerr != nil {
			//: report the terminal state.
			return rerr
		}
	}
	//: three opcodes, three obligations.
	switch header.OpCode {
	//: a liveness probe from the peer.
	case corenet.WSPing:
		//: §5.5.2 — the Pong must carry the SAME application data, which is
		//: what lets a peer correlate its probes rather than merely count them.
		return c.sendFrame(corenet.WSPong, payload)
	//: the answer to one of ours, or an unsolicited heartbeat.
	case corenet.WSPong:
		//: §5.5.3 — a Pong may answer our Ping or be an unsolicited heartbeat.
		//: Either way its ARRIVAL is the information; rx has already counted it.
		return nil
	//: the closing handshake.
	default:
		//: the only remaining control opcode is Close; the parser has already
		//: refused every reserved one.
		return c.serveClose(payload)
	}
}

// readControlPayload reads and unmasks one control frame's payload.
func (c *Conn) readControlPayload(payload []byte, key [corenet.WSMaskLen]byte) error {
	//: the socket is gone, or the peer announced more than it sent.
	if _, rerr := io.ReadFull(c.br, payload); rerr != nil {
		//: report the terminal state.
		return c.socketGone(rerr)
	}
	corenet.ApplyWSMask(payload, key)
	//: read.
	return nil
}

// serveClose completes the closing handshake the peer began.
func (c *Conn) serveClose(payload []byte) error {
	code, reason, perr := corenet.ParseWSClosePayload(payload)
	//: a one-byte payload, a reserved code, or a reason that is not UTF-8.
	if perr != nil {
		//: 1007 when the payload was the problem, 1002 when the framing was —
		//: the peer deserves to know which of the two it got wrong.
		return c.failConnection(closeCodeFor(perr), perr)
	}
	c.peerCode.Store(uint32(code))
	//: §5.5.1 — an endpoint that receives a Close and has not sent one MUST
	//: answer, and SHOULD echo the status code. A code that must not travel
	//: (the peer sent none, so we hold 1005) becomes a normal closure.
	swallowErr(c.sendClose(code.Echoable(), ""))
	//: §7.1.1 — once both sides have sent a Close, the server closes the TCP
	//: connection; the client is the one that waits.
	c.terminate()
	//: the terminal outcome, carrying what the peer said about it.
	return errs.Wrap(corenet.WSConnClosed, errs.WrapParams{},
		errs.Int("close_code", int(code)),
		errs.String("reason", reason))
}

// sendFrame writes one whole frame under the write lock.
func (c *Conn) sendFrame(op corenet.WSOpCode, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	//: the terminal check is inside the lock so it cannot pass while another
	//: goroutine is closing the connection between the check and the write.
	if eerr := c.ended(); eerr != nil {
		//: the connection is over.
		return eerr
	}
	//: one frame, whole.
	return c.writeLocked(op, payload)
}

// sendClose writes the connection's one and only Close frame.
func (c *Conn) sendClose(code corenet.WSCloseCode, reason string) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	//: §5.5.1 allows exactly one Close per endpoint; a second would arrive
	//: after the closing handshake completed, which is itself a violation.
	if c.closeSent {
		//: already said.
		return nil
	}
	var scratch [corenet.WSMaxControlPayload]byte
	payload, perr := corenet.AppendWSClosePayload(scratch[:0], code, reason)
	//: an unsendable code or an oversized reason.
	if perr != nil {
		//: nothing was written; report what the format cannot carry.
		return perr
	}
	//: marked before the write, so a failed write cannot leave the door open
	//: for a second attempt on a socket that is already gone.
	c.closeSent = true
	//: the frame itself.
	return c.writeLocked(corenet.WSClose, payload)
}

// writeLocked puts one encoded frame on the wire. The caller holds wmu.
func (c *Conn) writeLocked(op corenet.WSOpCode, payload []byte) error {
	frame, eerr := corenet.AppendWSFrame(c.wbuf[:0], op, true, payload)
	//: a reserved opcode or an oversized control frame; the buffer is untouched.
	if eerr != nil {
		//: nothing was written; report what the format cannot carry.
		return eerr
	}
	c.wbuf = frame
	//: refreshed per frame rather than set once: a connection has no total
	//: write budget by nature, but ONE write must still be bounded or a peer
	//: that stopped reading pins this goroutine and a socket buffer forever.
	swallowErr(c.raw.SetWriteDeadline(time.Now().Add(c.cfg.writeTimeout)))
	//: a failed write means the peer is gone; there is no recovering a frame
	//: stream whose reader has stopped listening.
	if _, werr := c.raw.Write(frame); werr != nil {
		c.terminate()
		//: report the terminal state with the cause attached.
		return errs.Wrap(corenet.WSConnClosed, errs.WrapParams{},
			errs.String("cause", werr.Error()))
	}
	//: delivered.
	return nil
}

// failConnection performs RFC 6455 §7.1.7: send a Close naming the fault when
// one can still be sent, then drop the connection.
//
// It returns the CAUSE rather than a generic closure, because the handler's
// only useful question is what the peer did wrong.
func (c *Conn) failConnection(code corenet.WSCloseCode, cause error) error {
	//: §7.1.7 says an endpoint MAY send a Close before failing. Doing so is
	//: what turns "the server hung up" into a diagnosable event on the other
	//: side, and it costs one frame.
	swallowErr(c.sendClose(code, ""))
	c.terminate()
	//: the violation itself, not a paraphrase of it.
	return cause
}

// socketGone reports a read that could not complete because the connection died.
func (c *Conn) socketGone(cause error) error {
	//: no Close frame: there is nothing left to send it on.
	c.terminate()
	//: the terminal state, with the socket's own reason attached.
	return errs.Wrap(corenet.WSConnClosed, errs.WrapParams{},
		errs.String("cause", cause.Error()))
}

// ended reports the connection's terminal error, or nil while it is open.
func (c *Conn) ended() error {
	select {
	//: the connection has ended for one of the reasons watch translates.
	case <-c.done:
		//: refuse the operation; the handler's loop reads this as "stop".
		return errs.Wrap(corenet.WSConnClosed, errs.WrapParams{})
	default:
	}
	//: still open.
	return nil
}

// end closes done exactly once, whichever goroutine gets there first.
func (c *Conn) end() {
	//: several goroutines can end a connection; only one may close the channel.
	c.endOnce.Do(func() {
		//: every observer — Receive, Send, Done, the heartbeat — sees this.
		close(c.done)
	})
}

// terminate ends the connection and closes the socket, without joining.
//
// It is separate from Close on purpose: the heartbeat and the watcher call it
// when their own work fails, and calling Close there would make a goroutine
// join itself.
func (c *Conn) terminate() {
	c.end()
	//: closing the socket is what actually unblocks a Receive parked in a read;
	//: the channel alone would leave it there until the peer sent something.
	swallowErr(c.raw.Close())
}

// join stops and waits for the connection's own goroutines.
func (c *Conn) join() {
	//: nil when the heartbeat was disabled, in which case there is nothing to
	//: join because nothing was started.
	if c.pinger != nil {
		c.pinger.Stop()
	}
	//: the watcher is always started, so it is always joined.
	c.watcher.Stop()
}

// watch starts the goroutine that turns the server's drain into a closing
// handshake.
//
// Goroutine lifecycle: exactly one per connection, owned by the Conn and joined
// by Close. It returns as soon as the connection ends, whatever ended it.
//
// The request context is deliberately NOT watched. net/http cancels it the
// moment the handler returns, and a hijacked socket outlives the handler by
// design — so a connection that watched it would end at an instant that says
// nothing about the peer. The drain signal is a channel captured at the
// upgrade, which is exactly why it keeps working afterwards.
func (c *Conn) watch(draining <-chan struct{}) {
	c.watcher = worker.Start(func(stop <-chan struct{}) {
		select {
		//: Close is joining us.
		case <-stop:
		//: the connection already ended some other way.
		case <-c.done:
		//: the server began draining. §7.4.1 has a code that says exactly
		//: this, and a peer told "going away" reconnects somewhere else
		//: instead of discovering a severed socket and guessing why.
		case <-draining:
			swallowErr(c.sendClose(corenet.WSCloseGoingAway, "server shutting down"))
			c.terminate()
		}
	})
}

// startHeartbeat spawns the periodic Ping, unless it is disabled.
//
// Goroutine lifecycle: at most one per connection, owned by the Conn and joined
// by Close. It exits on the connection's own end, so it cannot outlive the
// socket it writes to.
//
// It is the connection's ONLY liveness check. A peer that vanishes without
// closing — a lid closed, a NAT rebinding, a cable pulled — leaves a socket
// that is perfectly readable and will simply never produce another byte; no
// read error, no close frame, nothing. The heartbeat is what turns that silence
// into an ending.
//
// "Silence" means no frame READ since the last probe: the heartbeat watches
// rx, and only Receive moves it. It therefore cannot tell a vanished peer from
// a live one whose Pong is waiting, unread, behind a handler that stopped
// calling Receive — which is why a reading goroutine is part of Conn's
// contract rather than a style (ADR 0047 §D8, amended 2026-09-11).
func (c *Conn) startHeartbeat() {
	//: an explicitly disabled heartbeat starts no goroutine at all, so it costs
	//: nothing rather than costing a parked ticker.
	if c.cfg.noPing {
		//: nothing to start.
		return
	}
	c.pinger = worker.Start(func(stop <-chan struct{}) {
		//: the loop owns the ticker so it stops exactly when the loop returns.
		ticker := time.NewTicker(c.cfg.pingInterval)
		//: release the ticker once the loop returns.
		defer ticker.Stop()
		seen := c.rx.Load()
		probed := false
		//: probe until the connection ends or Close joins us.
		for {
			select {
			//: Close is joining us.
			case <-stop:
				//: nothing more to send.
				return
			//: the connection ended; the socket is no longer ours to write to.
			case <-c.done:
				//: nothing more to send.
				return
			case <-ticker.C:
				//: a peer that has not produced a single frame since we asked
				//: is not slow, it is gone: our Ping obliges it to answer.
				current := c.rx.Load()
				//: probed one full interval ago and not one frame since: a
				//: live peer is obliged to answer a Ping, so this silence is
				//: an ending rather than a lull.
				if probed && current == seen {
					c.terminate()
					//: the connection is over.
					return
				}
				seen = current
				//: a failed Ping has already terminated the connection.
				probed = c.Ping(nil) == nil
			}
		}
	})
}

// closeCodeFor picks the status a malformed Close payload deserves.
func closeCodeFor(cause error) corenet.WSCloseCode {
	//: a reason that is not UTF-8 is a payload fault, and §7.4.1 has a code for
	//: exactly that; reporting it as a framing error would send the peer
	//: looking in the wrong half of its implementation.
	if errs.HasCode(cause, corenet.CodeWSInvalidPayload) {
		//: 1007.
		return corenet.WSCloseInvalidPayload
	}
	//: everything else about a Close frame is shape, which is 1002.
	return corenet.WSCloseProtocolError
}

// swallowErr intentionally discards a non-actionable error, recording the
// discard so the error audit treats it as deliberate rather than dropped.
//
// It is used on three paths where there is genuinely nothing to report: sending
// a courtesy Close frame on a socket that may already be gone, closing that
// socket a second time, and setting a write deadline on a connection whose next
// write reports the real problem far more usefully.
func swallowErr(err error) {
	//: read the parameter so the unused-error audit treats this as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
	//: the error concerns a socket that is already finished with.
}
