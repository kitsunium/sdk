package sse

import (
	"sync"
	"sync/atomic"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// statusUnset is what a zero status means: the stream is accepted.
const statusUnset int = 0

// drainRetry is the reconnection hint a drain sends when the stream was given
// none: long enough for a restart, short enough to be a pause.
const drainRetry time.Duration = 3 * time.Second

var (
	// keepAliveFrame is the comment a keep-alive writes: nothing for a client to
	// parse, enough to keep a proxy from cutting an idle stream.
	keepAliveFrame = []byte(":\n\n")
	// loopHead are the response fields every event stream carries, written once.
	loopHead = []byte("Content-Type: " + corenet.SSEContentType + "\r\nCache-Control: no-cache\r\nX-Accel-Buffering: no\r\n")
)

// LoopHandler serves LoopSource as Server-Sent Events: the corenet.HTTP1Handler and
// corenet.HTTP1Streamer NewLoopHandler returns, for http1.HandleLoop to serve.
type LoopHandler struct {
	// src answers the program's half.
	src LoopSource
	// retry is the opening hint, zero for none; keepAlive the comment cadence,
	// zero for none.
	retry     time.Duration
	keepAlive time.Duration
	// scratch is each lane's reused frame buffer, found by lane with no lock.
	scratch atomic.Pointer[[]*[]byte]
	// mu serialises the first sight of a lane.
	mu sync.Mutex
}

// LoopStreamValue is one open event stream on the loop: a value kept for as long as
// the stream lives, every method running on its lane's thread.
type LoopStreamValue struct {
	// h is the handler that owns the lane's scratch.
	h *LoopHandler
	// s is the response delimited by close.
	s corenet.HTTP1StreamValue
}

// NewLoopHandler returns the handler that serves src as Server-Sent Events on a
// loop group, for http1.HandleLoop: the head is the same as New writes on the
// goroutine engine, the frames are AppendEvent's, the keep-alive comments are
// scheduled by the loop's timer, no goroutine per stream. A drain gives every
// stream a final retry: field and ends it. KeepAlive and Retry mean what they mean
// on New. WriteTimeout is refused with corenet.SSEStreamMisconfigured: the loop's
// OutputCap is what bounds a stream that stopped reading.
func NewLoopHandler(src LoopSource, opts ...Option) (*LoopHandler, error) {
	//: a source to ask.
	if src == nil {
		//: refused.
		return nil, errs.Wrap(corenet.SSEStreamMisconfigured, errs.WrapParams{}, errs.String("option", "source"))
	}
	cfg, err := resolve(opts)
	//: a negative interval, as on the goroutine engine.
	if err != nil {
		//: refused.
		return nil, err
	}
	//: a per-frame write budget has no shape on a loop.
	if hasWriteTimeout(opts) {
		//: refused, by name.
		return nil, errs.Wrap(corenet.SSEStreamMisconfigured, errs.WrapParams{}, errs.String("option", "WriteTimeout"),
			errs.String("why", "a loop's OutputCap bounds a stream that stopped reading"))
	}
	keepAlive := cfg.keepAlive
	//: the explicit spelling of "never".
	if cfg.noKeepAlive {
		keepAlive = 0
	}
	//: built once.
	return &LoopHandler{src: src, retry: cfg.retry, keepAlive: keepAlive}, nil
}

// hasWriteTimeout reports whether WriteTimeout was among opts, looked at before
// resolve's defaults fill it in.
func hasWriteTimeout(opts []Option) bool {
	var probe config
	//: applied in order, the last one winning, as resolve does.
	for _, opt := range opts {
		opt(&probe)
	}
	//: zero is "not given".
	return probe.writeTimeout != 0
}

// ServeHTTP1 implements corenet.HTTP1Handler: it asks the source, then answers
// as a stream.
func (h *LoopHandler) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	last, _ := req.Field(corenet.SSELastEventIDHeader)
	status := h.src.OpenSSE(req, last)
	//: refused by the program, with its own status.
	if status != statusUnset {
		res.Status = status
		//: a plain answer.
		return
	}
	res.Stream = true
	res.Header = append(res.Header, loopHead...)
}

// OnStreamOpen implements corenet.HTTP1Streamer: the opening retry hint, the
// first keep-alive, then the program's half.
func (h *LoopHandler) OnStreamOpen(s corenet.HTTP1StreamValue) {
	stream := LoopStreamValue{h: h, s: s}
	//: the opening hint, before any event.
	if h.retry > 0 {
		sendErr(stream.Send(corenet.SSEEventValue{Retry: corenet.DurationValue(h.retry)}))
	}
	h.armKeepAlive(s)
	h.src.StreamOpened(stream)
}

// OnStreamTimeout implements corenet.HTTP1Streamer: the keep-alive comment, and
// the next one armed.
func (h *LoopHandler) OnStreamTimeout(s corenet.HTTP1StreamValue) {
	//: a full output closes the stream: it stopped reading.
	if s.Send(keepAliveFrame) != nil {
		sendErr(s.End())
		//: nothing left to arm.
		return
	}
	h.armKeepAlive(s)
}

// OnStreamDrain implements corenet.HTTP1Streamer: a drain ends the stream with
// a final retry field, so the client reconnects somewhere else rather than at
// once.
func (h *LoopHandler) OnStreamDrain(s corenet.HTTP1StreamValue) {
	retry := h.retry
	//: a hint is what the client needs now whatever the stream was given.
	if retry <= 0 {
		retry = drainRetry
	}
	stream := LoopStreamValue{h: h, s: s}
	sendErr(stream.Send(corenet.SSEEventValue{Retry: corenet.DurationValue(retry)}))
	sendErr(s.End())
}

// OnStreamClose implements corenet.HTTP1Streamer; the program's half is told.
func (h *LoopHandler) OnStreamClose(s corenet.HTTP1StreamValue, err error) {
	h.src.StreamClosed(LoopStreamValue{h: h, s: s}, err)
}

// armKeepAlive sets the connection's timer for the next comment, unless asked
// for no keep-alive.
func (h *LoopHandler) armKeepAlive(s corenet.HTTP1StreamValue) {
	//: the explicit "never".
	if h.keepAlive > 0 {
		sendErr(s.Deadline(h.keepAlive))
	}
}

// Send writes one event's wire form, into the lane's reused buffer, nothing
// allocating once it has grown to the lane's largest frame. It returns what
// AppendEvent or the loop's Write returned: an invalid frame's refusal,
// LoopOutputFull past the connection's output ceiling, LoopStaleRef after a
// close.
func (s LoopStreamValue) Send(e corenet.SSEEventValue) error {
	buf := s.h.lane(s.s.IO.Loop())
	wire, err := AppendEvent((*buf)[:0], e)
	//: an invalid event writes nothing.
	if err != nil {
		//: refused, the buffer left as it was.
		return err
	}
	*buf = wire
	//: copied by Write, so the buffer is free again at once.
	return s.s.Send(wire)
}

// Conn names the stream's connection, the req.Conn of the request that opened it.
func (s LoopStreamValue) Conn() corenet.LoopConnRef {
	//: the connection's name.
	return s.s.Ref
}

// End closes the stream, once what was sent has left.
func (s LoopStreamValue) End() error {
	//: the loop's close.
	return s.s.End()
}

// lane returns lane i's frame buffer, built the first time it is asked for.
func (h *LoopHandler) lane(i int) *[]byte {
	//: the common case: no lock.
	if p := h.scratch.Load(); p != nil && i < len(*p) && (*p)[i] != nil {
		//: already there.
		return (*p)[i]
	}
	//: once per lane.
	return h.growLane(i)
}

// growLane builds lane i's buffer and publishes a table that holds it.
func (h *LoopHandler) growLane(i int) *[]byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	var cur []*[]byte
	//: the table so far.
	if p := h.scratch.Load(); p != nil {
		cur = *p
	}
	//: raced with nobody, since only lane i asks for i; checked all the same.
	if i < len(cur) && cur[i] != nil {
		//: already there.
		return cur[i]
	}
	next := make([]*[]byte, max(len(cur), i+1))
	copy(next, cur)
	next[i] = new(make([]byte, 0, initialFrameCapacity))
	h.scratch.Store(&next)
	//: published.
	return next[i]
}

// sendErr discards a write's refusal: a stream closing past its output ceiling,
// or already closed, is told by OnStreamClose, which is the one place to act.
func sendErr(err error) {
	//: read so the discard is visibly deliberate.
	if err != nil {
		//: nothing to do here.
		return
	}
}
