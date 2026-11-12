package websocket

import (
	"net/http"
	"sync"
	"sync/atomic"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// smallMessageCap is the reassembly buffer an idle connection may keep: a
// larger one is given back, so a burst does not stay on every connection.
const smallMessageCap int = 256

// The states of a connection's message reader.
const (
	// _ is no message open, the zero state.
	_ uint8 = iota
	// msgOpen is a message open, its current frame not ending it.
	msgOpen
	// msgOpenFinal is a message open, its current frame ending it.
	msgOpenFinal
)

var (
	// handshakeFields are the request fields verifyHandshake reads.
	handshakeFields = [...]string{"Upgrade", "Connection", corenet.WSVersionHeader, corenet.WSKeyHeader, "Origin", "Forwarded", "X-Forwarded-Proto", corenet.WSProtocolHeader}
	// connectionUpgradeField is the field the handshake's 101 needs, written once.
	upgradeField = []byte("Upgrade: websocket\r\nSec-WebSocket-Accept: ")
	// protocolField opens the subprotocol's line.
	protocolField = []byte("Sec-WebSocket-Protocol: ")
	// fieldEnd closes a field line.
	fieldEnd = []byte("\r\n")
)

// LoopHandler serves LoopConnHandler as WebSocket: the corenet.HTTP1Handler,
// corenet.HTTP1Streamer and corenet.HTTP1Upgrader NewLoopHandler returns, for
// http1.HandleLoop to serve.
type LoopHandler struct {
	// src is the program's half.
	src LoopConnHandler
	// cfg is the resolved option set.
	cfg config
	// lanes holds each lane's frame buffer and connection table.
	lanes atomic.Pointer[[]*wsLane]
	// mu serialises the first sight of a lane.
	mu sync.Mutex
}

// wsLane is one lane's state: a reused frame buffer and a connection state by
// ring and slot, pointer-free but for the message buffer a fragmented or large
// message borrows.
type wsLane struct {
	// out is the frame buffer every write of the lane reuses.
	out []byte
	// conns is a state per connection, by ring and slot.
	conns [][]wsConn
}

// wsConn is one connection's frame reader.
type wsConn struct {
	// gen is the generation of the connection the state describes.
	gen uint32
	// msg reassembles a message that does not fit one read: nil while idle, so an
	// idle connection costs nothing but this value.
	msg []byte
	// frameLeft is how much of the current data frame's payload is still to come.
	frameLeft uint64
	// maskPos is the mask's position within the current data frame.
	maskPos uint64
	// key is the current data frame's mask key.
	key [MaskLen]byte
	// op is the message's opcode, the first frame's, every continuation inheriting it.
	op corenet.WSOpCode
	// msgState is whether a message is open and whether the current frame ends it.
	msgState uint8
	// awaiting says a ping is out with no frame received since.
	awaiting bool
	// dead says the connection is being closed: bytes are dropped.
	dead bool
	// subprotocol is the agreed subprotocol's index plus one, zero for none.
	subprotocol uint8
}

// LoopConnValue is one open WebSocket connection on the loop, a value kept for as
// long as it lives, every method running on its lane's thread.
type LoopConnValue struct {
	// h is the handler that owns the lane's frame buffer.
	h *LoopHandler
	// s is the connection.
	s corenet.HTTP1StreamValue
}

// NewLoopHandler returns the handler that serves src as WebSocket connections
// on a loop group, for http1.HandleLoop: the handshake checks of Upgrade (RFC
// 6455 §4.2.1, the origin policy of ADR 0069 and its options included), a 101
// through the http1 adapter, then every frame parsed from the connection's
// bytes with no goroutine per connection. A frame's announced length is judged
// before anything is copied, a Ping answered on the spot, the heartbeat run on
// the loop's timer, a drain closed 1001. WriteTimeout is refused with
// corenet.WSConnMisconfigured: the loop's OutputCap bounds a connection that
// stopped reading.
func NewLoopHandler(src LoopConnHandler, opts ...Option) (*LoopHandler, error) {
	//: a program to ask.
	if src == nil {
		//: refused.
		return nil, errs.Wrap(corenet.WSConnMisconfigured, errs.WrapParams{}, errs.String("option", "source"))
	}
	var probe config
	//: the options as given, before the defaults fill them.
	for _, opt := range opts {
		opt(&probe)
	}
	//: a per-frame write budget has no shape on a loop.
	if probe.writeTimeout != 0 {
		//: refused, by name.
		return nil, errs.Wrap(corenet.WSConnMisconfigured, errs.WrapParams{}, errs.String("option", "WriteTimeout"),
			errs.String("why", "a loop's OutputCap bounds a connection that stopped reading"))
	}
	cfg, err := resolve(opts)
	//: a value resolve refuses.
	if err != nil {
		//: refused.
		return nil, err
	}
	//: built once.
	return &LoopHandler{src: src, cfg: cfg}, nil
}

// statusRecorder is the small http.ResponseWriter verifyHandshake answers
// through, so a handshake on the loop is judged by the same rules as Upgrade's.
type statusRecorder struct {
	// header collects the refusal's fields.
	header http.Header
	// status is the refusal's status.
	status int
}

// Header implements http.ResponseWriter.
func (r *statusRecorder) Header() http.Header {
	//: the refusal's own fields.
	return r.header
}

// Write implements http.ResponseWriter; a refusal's body is not this package's to send here.
func (r *statusRecorder) Write(p []byte) (int, error) {
	//: discarded: the profile's own refusal is the answer.
	return len(p), nil
}

// WriteHeader implements http.ResponseWriter.
func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
}

// shimRequest builds what verifyHandshake reads from a request framed by the
// codec: only the fields the handshake looks at.
func shimRequest(req *corenet.HTTP1RequestValue) *http.Request {
	h := make(http.Header, 6)
	//: each field the handshake reads, copied since verifyHandshake keeps nothing past the call.
	for _, name := range handshakeFields {
		copyField(h, req, name)
	}
	method, host := string(req.Method), string(req.Host)
	//: the Host the origin rule compares against, and HTTP/1.1, which is all the profile speaks.
	return &http.Request{Method: method, Host: host, Header: h, ProtoMajor: 1, ProtoMinor: 1}
}

// copyField copies one request field into h: absent stays absent, so a rule
// that reads the field's presence keeps its meaning.
func copyField(h http.Header, req *corenet.HTTP1RequestValue, name string) {
	value, ok := req.Field(name)
	//: a field the client did not send.
	if !ok {
		//: left out.
		return
	}
	h.Add(name, string(value))
}

// ServeHTTP1 implements corenet.HTTP1Handler: the handshake, judged as Upgrade
// judges it, then the program's OpenWS.
func (h *LoopHandler) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	r := shimRequest(req)
	rec := &statusRecorder{header: http.Header{}, status: http.StatusBadRequest}
	//: Upgrade's own checks, so a handshake answers alike on both engines.
	if err := verifyHandshake(rec, r, &h.cfg); err != nil {
		res.Status = rec.status
		res.Header = appendRefusalFields(res.Header, rec.header)
		//: refused.
		return
	}
	status := h.src.OpenWS(req)
	//: the program's own refusal.
	if status != 0 {
		res.Status = status
		//: refused.
		return
	}
	key := r.Header.Get(corenet.WSKeyHeader)
	chosen, agreed := chooseSubprotocol(h.cfg.subprotocols, r)
	out := append(res.Header, upgradeField...)
	out = append(out, AcceptKey(key)...)
	out = append(out, fieldEnd...)
	//: only when one was agreed: RFC 6455 §4.2.2 reads no field as "none".
	if agreed {
		out = append(out, protocolField...)
		out = append(out, chosen...)
		out = append(out, fieldEnd...)
	}
	res.Upgrade, res.Header = true, out
	h.markSubprotocol(req, chosen, agreed)
}

// appendRefusalFields spells the fields a refusal carries.
func appendRefusalFields(dst []byte, h http.Header) []byte {
	//: a Sec-WebSocket-Version, on a version a client should renegotiate.
	for name, values := range h {
		//: each value.
		for _, v := range values {
			dst = append(dst, name...)
			dst = append(dst, ": "...)
			dst = append(dst, v...)
			dst = append(dst, fieldEnd...)
		}
	}
	//: as many as verifyHandshake set.
	return dst
}

// markSubprotocol records the agreed subprotocol against the connection, by its
// position in the configured list, for OnStreamOpen to name.
func (h *LoopHandler) markSubprotocol(req *corenet.HTTP1RequestValue, chosen string, agreed bool) {
	ws := h.conn(req.Lane, req.Conn)
	ws.subprotocol = 0
	//: nothing agreed.
	if !agreed {
		//: leave it at none.
		return
	}
	//: position in the server's list, so the state stays pointer-free.
	for i, name := range h.cfg.subprotocols {
		//: the one chosen.
		if name == chosen {
			ws.subprotocol = uint8(i + 1)
			//: found.
			return
		}
	}
}

// OnStreamOpen implements corenet.HTTP1Streamer.
func (h *LoopHandler) OnStreamOpen(s corenet.HTTP1StreamValue) {
	ws := h.conn(s.IO.Loop(), s.Ref)
	sub := ""
	//: the subprotocol the handshake recorded.
	if ws.subprotocol > 0 {
		sub = h.cfg.subprotocols[ws.subprotocol-1]
	}
	h.armHeartbeat(s)
	h.src.WSOpened(LoopConnValue{h: h, s: s}, sub)
}

// armHeartbeat sets the connection's timer for the next heartbeat, unless asked
// for none.
func (h *LoopHandler) armHeartbeat(s corenet.HTTP1StreamValue) {
	//: the explicit "never".
	if !h.cfg.noPing && h.cfg.pingInterval > 0 {
		swallowErr(s.Deadline(h.cfg.pingInterval))
	}
}

// OnStreamTimeout implements corenet.HTTP1Streamer, the heartbeat's tick.
func (h *LoopHandler) OnStreamTimeout(s corenet.HTTP1StreamValue) {
	ws := h.conn(s.IO.Loop(), s.Ref)
	//: the last ping never came back and nothing else arrived either.
	if ws.awaiting {
		ws.dead = true
		swallowErr(s.End())
		//: the connection is over.
		return
	}
	ws.awaiting = true
	swallowErr(LoopConnValue{h: h, s: s}.Ping(nil))
	h.armHeartbeat(s)
}

// OnStreamDrain implements corenet.HTTP1Streamer, a 1001 close.
func (h *LoopHandler) OnStreamDrain(s corenet.HTTP1StreamValue) {
	ws := h.conn(s.IO.Loop(), s.Ref)
	swallowErr(h.closeWith(s, ws, corenet.WSCloseGoingAway, ""))
}

// OnStreamClose implements corenet.HTTP1Streamer.
func (h *LoopHandler) OnStreamClose(s corenet.HTTP1StreamValue, err error) {
	h.src.WSClosed(LoopConnValue{h: h, s: s}, err)
	ws := h.conn(s.IO.Loop(), s.Ref)
	*ws = wsConn{gen: ws.gen}
}

// lane returns lane i's state, built the first time it is asked for.
func (h *LoopHandler) lane(i int) *wsLane {
	//: the common case: no lock.
	if p := h.lanes.Load(); p != nil && i < len(*p) && (*p)[i] != nil {
		//: already there.
		return (*p)[i]
	}
	//: once per lane.
	return h.growLane(i)
}

// growLane builds lane i's state and publishes a table that holds it.
func (h *LoopHandler) growLane(i int) *wsLane {
	h.mu.Lock()
	defer h.mu.Unlock()
	var cur []*wsLane
	//: the table so far.
	if p := h.lanes.Load(); p != nil {
		cur = *p
	}
	//: raced with nobody; checked all the same.
	if i < len(cur) && cur[i] != nil {
		//: already there.
		return cur[i]
	}
	next := make([]*wsLane, max(len(cur), i+1))
	copy(next, cur)
	next[i] = &wsLane{}
	h.lanes.Store(&next)
	//: published.
	return next[i]
}

// conn returns ref's state on lane, growing the table on first sight; a slot's
// older generation starts clean.
func (h *LoopHandler) conn(lane int, ref corenet.LoopConnRef) *wsConn {
	st := h.lane(lane)
	ring, slot := int(ref.Ring), int(ref.Slot)
	//: a ring seen for the first time.
	if ring >= len(st.conns) {
		st.conns = append(st.conns, make([][]wsConn, ring+1-len(st.conns))...)
	}
	//: a slot past the table: grown by doubling.
	if slot >= len(st.conns[ring]) {
		grown := make([]wsConn, max(slot+1, 2*len(st.conns[ring])))
		copy(grown, st.conns[ring])
		st.conns[ring] = grown
	}
	ws := &st.conns[ring][slot]
	//: a slot reused by a newer connection starts clean.
	if ws.gen != ref.Gen {
		*ws = wsConn{gen: ref.Gen}
	}
	//: the connection's own.
	return ws
}

// NewLoopConnValue is the value OnStreamOpen hands the program for connection s,
// a handler's own test double being the one other caller.
func NewLoopConnValue(h *LoopHandler, s corenet.HTTP1StreamValue) LoopConnValue {
	//: the two halves of the value.
	return LoopConnValue{h: h, s: s}
}

// Conn names the connection.
func (c LoopConnValue) Conn() corenet.LoopConnRef {
	//: the connection's name.
	return c.s.Ref
}

// Send writes one message as a single frame, refused as LoopIO.Write is.
func (c LoopConnValue) Send(m corenet.WSMessageValue) error {
	op := corenet.WSText
	//: the message's own kind.
	if m.Binary {
		op = corenet.WSBinary
	}
	//: one frame, unfragmented.
	return c.frame(op, m.Data)
}

// Ping writes a ping frame; payload is at most what a control frame carries.
func (c LoopConnValue) Ping(payload []byte) error {
	//: the opcode's own frame.
	return c.frame(corenet.WSPing, payload)
}

// frame appends one frame to the lane's buffer and writes it.
func (c LoopConnValue) frame(op corenet.WSOpCode, payload []byte) error {
	lane := c.h.lane(c.s.IO.Loop())
	wire, err := AppendFrame(lane.out[:0], op, true, payload)
	//: a frame the codec refuses writes nothing.
	if err != nil {
		//: refused.
		return err
	}
	lane.out = wire
	//: copied by Write, so the buffer is free at once.
	return c.s.Send(wire)
}

// CloseWith starts the closing handshake with code and reason, and ends the connection.
func (c LoopConnValue) CloseWith(code corenet.WSCloseCode, reason string) error {
	ws := c.h.conn(c.s.IO.Loop(), c.s.Ref)
	//: the handler's own close, the connection stopped reading.
	return c.h.closeWith(c.s, ws, code, reason)
}

// closeWith sends the close frame once and ends the connection.
func (h *LoopHandler) closeWith(s corenet.HTTP1StreamValue, ws *wsConn, code corenet.WSCloseCode, reason string) error {
	//: said once; bytes after it are dropped.
	if ws.dead {
		//: already over.
		return nil
	}
	ws.dead = true
	lane := h.lane(s.IO.Loop())
	payload, err := AppendClosePayload(lane.out[:0], code, reason)
	//: a code the codec refuses still ends the connection.
	if err == nil {
		wire, ferr := AppendFrame(nil, corenet.WSClose, true, payload)
		//: written, in place of any further frame.
		if ferr == nil {
			swallowErr(s.Send(wire))
		}
	}
	//: closed once the close frame has left.
	return s.End()
}

// OnUpgradedData implements corenet.HTTP1Upgrader, the frames after the 101.
func (h *LoopHandler) OnUpgradedData(s corenet.HTTP1StreamValue, buf []byte) int {
	ws := h.conn(s.IO.Loop(), s.Ref)
	//: a connection being closed reads nothing more.
	if ws.dead {
		//: dropped.
		return len(buf)
	}
	ws.awaiting = false
	used := 0
	//: frame after frame.
	for used < len(buf) && !ws.dead {
		n, more := h.step(s, ws, buf[used:])
		used += n
		//: the frame needs bytes that have not come.
		if !more {
			break
		}
	}
	//: a connection that just died takes the rest of the buffer with it.
	if ws.dead {
		//: dropped.
		return len(buf)
	}
	//: the rest waits in the spill.
	return used
}

// step advances the frame reader by whatever buf holds and reports the bytes it
// took and whether to go on.
func (h *LoopHandler) step(s corenet.HTTP1StreamValue, ws *wsConn, buf []byte) (int, bool) {
	//: the rest of a data frame's payload.
	if ws.frameLeft > 0 {
		return h.payload(s, ws, buf)
	}
	//: not enough for even the length byte.
	if len(buf) < MinHeaderLen {
		//: needs more.
		return 0, false
	}
	want := FrameHeaderLen(buf[:MinHeaderLen])
	//: the rest of the header.
	if len(buf) < want {
		//: needs more.
		return 0, false
	}
	header, herr := ParseFrameHeader(buf[:want])
	//: reserved bits, opcodes, control-frame shape, non-minimal length.
	if herr != nil {
		h.fail(s, ws, corenet.WSCloseProtocolError)
		//: fatal.
		return len(buf), false
	}
	//: §5.1: every client frame is masked.
	if merr := header.ValidateFromClient(); merr != nil {
		h.fail(s, ws, corenet.WSCloseProtocolError)
		//: fatal.
		return len(buf), false
	}
	//: judged on what the frame announces, before a byte of it is held.
	if !header.OpCode.IsControl() && header.Length > uint64(h.cfg.maxFrameSize) {
		h.fail(s, ws, corenet.WSCloseTooLarge)
		//: fatal.
		return len(buf), false
	}
	//: a control frame is whole or waits: it is at most 125 bytes.
	if header.OpCode.IsControl() {
		return h.control(s, ws, buf, want, header)
	}
	//: a data frame: its fragmentation, then its payload as it comes.
	if !h.startData(s, ws, header) {
		//: fatal.
		return len(buf), false
	}
	return h.payloadFrom(s, ws, buf, want)
}

// startData applies §5.4's ordering rules and the message ceiling to a data frame.
func (h *LoopHandler) startData(s corenet.HTTP1StreamValue, ws *wsConn, header FrameHeaderValue) bool {
	//: a continuation continues something.
	if header.OpCode == corenet.WSContinuation {
		//: an orphan continuation.
		if ws.msgState == 0 {
			h.fail(s, ws, corenet.WSCloseProtocolError)
			//: fatal.
			return false
		}
	} else {
		//: a new message while one is open.
		if ws.msgState != 0 {
			h.fail(s, ws, corenet.WSCloseProtocolError)
			//: fatal.
			return false
		}
		ws.op, ws.msg = header.OpCode, ws.msg[:0]
	}
	//: the accumulated total the ceiling is about.
	if int64(len(ws.msg))+int64(header.Length) > h.cfg.maxMessageSize {
		h.fail(s, ws, corenet.WSCloseTooLarge)
		//: fatal.
		return false
	}
	ws.frameLeft, ws.key, ws.maskPos = header.Length, header.MaskKey, 0
	ws.msgState = msgOpen
	//: this frame ends the message.
	if header.Final {
		ws.msgState = msgOpenFinal
	}
	//: an empty data frame ends right here, if it is final.
	if header.Length == 0 && header.Final {
		h.complete(s, ws, nil)
	}
	//: accepted.
	return true
}

// payloadFrom skips the header and takes the frame's payload from buf.
func (h *LoopHandler) payloadFrom(s corenet.HTTP1StreamValue, ws *wsConn, buf []byte, header int) (int, bool) {
	n, more := h.payload(s, ws, buf[header:])
	//: the header's bytes count with the payload's.
	return header + n, more
}

// payload unmasks what buf holds of the current data frame into the message,
// delivering a whole message that fits this read in place, without a copy.
func (h *LoopHandler) payload(s corenet.HTTP1StreamValue, ws *wsConn, buf []byte) (int, bool) {
	take := int(min(uint64(len(buf)), ws.frameLeft))
	chunk := buf[:take]
	applyMaskAt(chunk, ws.key, ws.maskPos)
	ws.frameLeft -= uint64(take)
	ws.maskPos += uint64(take)
	//: a frame still arriving.
	if ws.frameLeft > 0 {
		ws.msg = append(ws.msg, chunk...)
		//: needs more.
		return take, false
	}
	//: a fragment: kept, the message goes on.
	if ws.msgState != msgOpenFinal {
		ws.msg = append(ws.msg, chunk...)
		//: on to the next frame.
		return take, true
	}
	//: the common case: a whole message in one read, handed out in place.
	if len(ws.msg) == 0 {
		h.complete(s, ws, chunk)
	} else {
		ws.msg = append(ws.msg, chunk...)
		h.complete(s, ws, ws.msg)
	}
	//: on to the next frame, unless the message closed the connection.
	return take, !ws.dead
}

// complete validates a whole message and hands it to the program; a large
// reassembly buffer is given back so an idle connection stays small.
func (h *LoopHandler) complete(s corenet.HTTP1StreamValue, ws *wsConn, data []byte) {
	ws.msgState = 0
	//: UTF-8 is judged on the whole message, as a sequence may straddle a fragment.
	if ws.op == corenet.WSText && ValidateText(data) != nil {
		h.fail(s, ws, corenet.WSCloseInvalidPayload)
		//: fatal.
		return
	}
	h.src.WSMessage(LoopConnValue{h: h, s: s}, corenet.WSMessageValue{Binary: ws.op == corenet.WSBinary, Data: data})
	//: a reassembly buffer past the small case goes back to the runtime.
	if cap(ws.msg) > smallMessageCap {
		ws.msg = nil
	} else {
		ws.msg = ws.msg[:0]
	}
}

// control serves one whole control frame in place, or waits for the rest of it.
func (h *LoopHandler) control(s corenet.HTTP1StreamValue, ws *wsConn, buf []byte, header int, f FrameHeaderValue) (int, bool) {
	end := header + int(f.Length)
	//: a control frame is at most 125 bytes: it waits to be whole.
	if len(buf) < end {
		//: needs more.
		return 0, false
	}
	payload := buf[header:end]
	ApplyMask(payload, f.MaskKey)
	//: three opcodes, three obligations.
	switch f.OpCode {
	case corenet.WSPing:
		//: §5.5.3 — the pong carries the ping's own bytes.
		swallowErr(LoopConnValue{h: h, s: s}.frame(corenet.WSPong, payload))
	case corenet.WSPong:
		//: a heartbeat answered, or unsolicited: nothing to do.
	case corenet.WSClose:
		h.serveClose(s, ws, payload)
	default:
		//: ParseFrameHeader refused every other opcode already.
	}
	//: on to the next frame, unless the close ended the connection.
	return end, !ws.dead
}

// serveClose echoes a peer's close and ends the connection.
func (h *LoopHandler) serveClose(s corenet.HTTP1StreamValue, ws *wsConn, payload []byte) {
	code, _, perr := ParseClosePayload(payload)
	//: a one-byte payload, a reserved code, a reason that is not UTF-8.
	if perr != nil {
		h.fail(s, ws, closeCodeFor(perr))
		//: fatal.
		return
	}
	swallowErr(h.closeWith(s, ws, code.Echoable(), ""))
}

// fail closes the connection with code, as failConnection does on the goroutine
// engine.
func (h *LoopHandler) fail(s corenet.HTTP1StreamValue, ws *wsConn, code corenet.WSCloseCode) {
	swallowErr(h.closeWith(s, ws, code, ""))
}

// applyMaskAt unmasks payload, which starts at offset pos of its frame.
func applyMaskAt(payload []byte, key [MaskLen]byte, pos uint64) {
	var rotated [MaskLen]byte
	//: the key rotated to where this chunk starts in the frame.
	for i := range rotated {
		rotated[i] = key[(uint64(i)+pos)%uint64(MaskLen)]
	}
	ApplyMask(payload, rotated)
}
