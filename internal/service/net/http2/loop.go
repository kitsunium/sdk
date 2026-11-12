package http2

import (
	"bytes"
	"sync"
	"sync/atomic"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// What a connection's first bytes made of it.
const (
	// kindSniffing is a connection whose bytes so far match the preface, or
	// which sent none.
	kindSniffing connKind = iota
	// kindHTTP1 is a connection the embedded HTTP/1.1 adapter serves.
	kindHTTP1
	// kindHTTP2 is a connection speaking HTTP/2.
	kindHTTP2
	// kindRefused is a connection answered 400 and closing.
	kindRefused
)

// protoHTTP1 and protoH2 are the group's two ALPN identifiers.
const (
	// protoHTTP1 is HTTP/1.1's identifier.
	protoHTTP1 string = "http/1.1"
	// protoH2 is HTTP/2's identifier.
	protoH2 string = "h2"
)

// refusalPreface is the whole answer to a connection speaking a version the
// group does not serve: a 400, and the connection closed.
var refusalPreface = []byte("HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")

// connKind is what a connection's first bytes made of it.
type connKind uint8

// HandleLoop adapts h to the loop engine for HTTP/2 beside HTTP/1.1: the
// corenet.LoopHandler a group serves with StreamGroup.HandleLoop. Each
// connection's first bytes decide: the 24-byte client preface makes it an
// HTTP/2 connection, served by a state machine of this package and h on the
// lane's thread; anything else goes to an embedded http1.HandleLoop(h, p),
// with no cost to it past that one comparison.
//
// One chain serves both versions: an HTTP/2 request is mapped onto h's
// corenet.HTTP1RequestValue, :method to Method, :path to Target, :authority to
// Host, its regular fields in Fields, and judged by the same canonical target
// as HTTP/1.1, so Routes, Bearer, Headers and Log see one value. A request is
// served once its last frame has arrived, its body handed whole, or chunk by
// chunk when h is a corenet.HTTP1BodyReader. Answers are written at the batch's
// end, after h's EndBatch when h is a corenet.HTTP1Batcher; a Pending answer is
// the lane's worker's, the rest of the connection's streams going on meanwhile.
//
// h2c is by prior knowledge only: the Upgrade: h2c form is not served, a
// request carrying it answered as plain HTTP/1.1. The group's Protocols names
// what it serves: h2 alone answers 400 and closes a connection that does not
// open with the preface, http/1.1 alone answers 400 and closes one that does.
// The operator's http2-loop switch has the second effect. Without a Protocols
// option both versions are served.
//
// On a TLS group (portable backend, ADR 0198) ALPN offers h2 then http/1.1,
// or the Protocols list, narrowed by the identity's NextProtos, and the
// adapter serves what ALPN offers; the version is still chosen by the
// preface, the negotiated protocol not consulted, so RFC 9113 §3.3 is not
// enforced: a client that negotiated h2 and speaks HTTP/1.1 is served
// HTTP/1.1 when the list holds it, and answered 400 otherwise.
func HandleLoop(h corenet.HTTP1Handler, p http1.Parser) *LoopHandler {
	w, _ := h.(corenet.HTTP1Worker)
	r, _ := h.(corenet.HTTP1BodyReader)
	ab, _ := h.(corenet.HTTP1BodyAborter)
	//: found once, never per batch: both adapters share h.
	return &LoopHandler{
		h: h, p: p, h1: http1.HandleLoop(h, p), worker: w, reader: r, aborter: ab,
		serveH1: true, serveH2: true, maxBody: p.Limits().Body, holdCap: defaultHoldCap,
	}
}

// NewLoopHandler is HandleLoop, under the name constructors take; see it.
func NewLoopHandler(h corenet.HTTP1Handler, p http1.Parser) *LoopHandler {
	//: the adapter HandleLoop documents.
	return HandleLoop(h, p)
}

// LoopHandler serves HTTP/2 and HTTP/1.1 on one loop group: the
// corenet.LoopHandler HandleLoop returns. Its state is per lane and a lane
// touches only its own, so the data path takes no lock.
type LoopHandler struct {
	// h serves every request of both versions.
	h corenet.HTTP1Handler
	// p frames HTTP/1.1 requests and bounds the head of a request of both.
	p http1.Parser
	// h1 serves the connections that do not open with the preface.
	h1 *http1.LoopHandler
	// worker, reader and aborter are h's optional capabilities, nil when it
	// lacks them; its HTTP1Batcher is the embedded adapter's to call, once a
	// batch for both versions.
	worker  corenet.HTTP1Worker
	reader  corenet.HTTP1BodyReader
	aborter corenet.HTTP1BodyAborter
	// limits are HTTP/2's bounds, as a group option gave them.
	limits corehttp2.LimitsValue
	// serveH1 and serveH2 say which versions the group serves.
	serveH1, serveH2 bool
	// maxBody bounds a request's body.
	maxBody int64
	// holdCap bounds what one connection holds unsent.
	holdCap int
	// lanes holds each lane's state at its LoopIO.Loop index, replaced under mu.
	lanes atomic.Pointer[[]*laneState]
	// mu serialises the first sight of a lane.
	mu sync.Mutex
	// draining is set once the group's drain began.
	draining atomic.Bool
	// requests counts the HTTP/2 requests served, added once a batch.
	requests atomic.Uint64
	// errs counts the HTTP/2 errors by corehttp2.ErrorType, added once a batch.
	errs [corehttp2.ErrorTypes]atomic.Uint64
}

// OnStart implements corenet.LoopStarter: it builds every lane's state
// before the first callback.
func (a *LoopHandler) OnStart(lanes int) {
	a.h1.OnStart(lanes)
	//: each lane's own.
	for i := range lanes {
		a.state(i)
	}
}

// Honours implements corenet.LoopOptionHonourer: Protocols, which the group
// hands over through SetHTTP.
func (*LoopHandler) Honours(k corenet.OptionKind) bool {
	//: the two options the adapter applies itself.
	return k == corenet.OptionProtocols || k == corenet.OptionHTTP2
}

// SetHTTP implements corenet.LoopHTTPCodec. It keeps the versions the group
// serves; the operator's switch leaves HTTP/1.1 alone.
func (a *LoopHandler) SetHTTP(v corenet.LoopHTTPValue) {
	a.limits = v.HTTP2
	//: no Protocols option: both.
	if v.Protocols != nil {
		a.serveH1, a.serveH2 = false, false
		//: each version the list names.
		for _, p := range v.Protocols {
			a.serveH1 = a.serveH1 || p == protoHTTP1
			a.serveH2 = a.serveH2 || p == protoH2
		}
	}
	//: switched off by the operator: the preface is refused like an unnamed version.
	if v.HTTP2Off {
		a.serveH2 = false
	}
}

// CheckSpill implements corenet.LoopSpillChecker: HTTP/1.1's own bounds, and
// the most one HTTP/2 frame or field block the spill must hold.
func (a *LoopHandler) CheckSpill(spillCap int) error {
	//: HTTP/1.1's check first: a request's body and head.
	if err := a.h1.CheckSpill(spillCap); err != nil {
		//: its refusal.
		return err
	}
	//: nothing of HTTP/2's own to fit: the state machine's bounds are checked at SetHTTP.
	return a.checkH2Spill(spillCap)
}

// checkH2Spill refuses a group whose spill cannot hold the largest frame or
// field block HTTP/2's bounds admit: a frame incomplete in the spill stays
// there whole.
func (a *LoopHandler) checkH2Spill(spillCap int) error {
	limits, err := a.limits.Resolve()
	//: a bound that cannot be honoured, named by Resolve.
	if err != nil {
		//: refused.
		return err
	}
	frames := int(maxContinuationFrames) + 1
	need := max(limits.MaxFrame, blockBoundFactor*limits.MaxHeaderList+frames*corehttp2.FrameHeaderLen)
	//: a spill too small for one whole frame or field block.
	if spillCap < need+corehttp2.FrameHeaderLen {
		//: refused, naming the bound.
		return errs.Wrap(corehttp2.LimitsRefused, errs.WrapParams{},
			errs.String("reason", "spill"), errs.Int("spill_cap", spillCap), errs.Int("needed", need+corehttp2.FrameHeaderLen))
	}
	//: the spill holds it.
	return nil
}

// Unwrap returns the HTTP1Handler the adapter serves.
func (a *LoopHandler) Unwrap() corenet.HTTP1Handler {
	//: the handler given to HandleLoop, as the embedded adapter holds it.
	return a.h1.Unwrap()
}

// OnOpen implements corenet.LoopHandler.
func (a *LoopHandler) OnOpen(io corenet.LoopIO, ref corenet.LoopConnRef) {
	st := a.state(io.Loop())
	st.remember(io)
	e := st.entry(ref)
	e.kind, e.gen, e.flags = kindSniffing, ref.Gen, entryLive
	e.requests, e.buffered = noIndex, 0
}

// OnData implements corenet.LoopHandler: the first bytes decide the
// connection's version, then every byte goes to its adapter.
func (a *LoopHandler) OnData(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	st := a.state(io.Loop())
	e := st.entry(ref)
	//: the verdict on this connection, when its first bytes are still to judge.
	if e.kind == kindSniffing {
		ready, used := a.sniff(io, ref, e, buf)
		//: wait for the rest of the preface, or the connection is being refused.
		if !ready {
			//: consumed what the sniff decided.
			return used
		}
	}
	//: by the version it speaks.
	switch e.kind {
	case kindHTTP1:
		//: every byte goes to the HTTP/1.1 adapter.
		return a.h1.OnData(io, ref, buf)
	case kindHTTP2:
		//: Feed and the stream events.
		return a.feed(io, ref, st, e, buf)
	default:
		//: a refused connection reads nothing more.
		return len(buf)
	}
}

// sniff judges a connection's first bytes against the preface and reports
// whether its version is decided: when it is not, the sniff consumed what the
// second result says.
func (a *LoopHandler) sniff(io corenet.LoopIO, ref corenet.LoopConnRef, e *connEntry, buf []byte) (bool, int) {
	n := min(len(buf), clientPrefaceLen)
	match := bytes.Equal(buf[:n], clientPrefaceBytes[:n])
	//: a preface still arriving: nothing yet says what this connection is.
	if match && n < clientPrefaceLen {
		//: waits in the spill for the rest.
		return false, 0
	}
	//: by the verdict and what the group serves.
	switch {
	case match && a.serveH2:
		st := a.state(io.Loop())
		st.lane.Open(&e.conn)
		e.kind = kindHTTP2
		//: decided.
		return true, 0
	case !match && a.serveH1:
		a.h1.OnOpen(io, ref)
		e.kind = kindHTTP1
		//: decided.
		return true, 0
	default:
		//: a version the group does not serve: 400, closed.
		e.kind = kindRefused
		a.writeClose(io, ref, refusalPreface, true)
		//: nothing more is read.
		return false, len(buf)
	}
}

// writeClose writes p and closes ref when closing, or when the write is
// refused.
func (*LoopHandler) writeClose(io corenet.LoopIO, ref corenet.LoopConnRef, p []byte, closing bool) {
	//: a refused write ends the connection, as a refused http1 answer does.
	if err := io.Write(ref, p); err == nil && !closing {
		//: written, kept open.
		return
	}
	//: a stale reference is the only refusal, and means already closed.
	swallowErr(io.Close(ref))
}

// OnTimeout implements corenet.LoopHandler. The adapter arms no deadline, so
// a timeout it did not ask for closes the connection.
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

// OnClose implements corenet.LoopHandler: the connection's state is released
// and a request still reading its body abandoned.
func (a *LoopHandler) OnClose(io corenet.LoopIO, ref corenet.LoopConnRef, err error) {
	st := a.state(io.Loop())
	e := st.entry(ref)
	//: an HTTP/1.1 connection is the embedded adapter's to clean.
	if e.kind == kindHTTP1 {
		a.h1.OnClose(io, ref, err)
	}
	//: an HTTP/2 connection's streams and arenas.
	if e.kind == kindHTTP2 {
		st.closeRequests(a, e)
		st.lane.Close(&e.conn)
	}
	e.kind, e.flags = kindRefused, 0
}

// state returns lane i's state, built on the lane's first call.
func (a *LoopHandler) state(i int) *laneState {
	//: the common case: the lane has been seen.
	if p := a.lanes.Load(); p != nil && i < len(*p) && (*p)[i] != nil {
		//: no lock taken.
		return (*p)[i]
	}
	//: once per lane.
	return a.grow(i)
}

// grow builds lane i's state and publishes a table that holds it. A lane that
// cannot be built holds no state and serves no HTTP/2: Honours and
// CheckSpill have already judged the limits at Start, so this happens only
// when a lane is first seen after a change the Start would have refused.
func (a *LoopHandler) grow(i int) *laneState {
	a.mu.Lock()
	defer a.mu.Unlock()
	var cur []*laneState
	//: the table as the other lanes left it.
	if p := a.lanes.Load(); p != nil {
		cur = *p
	}
	//: built meanwhile.
	if i < len(cur) && cur[i] != nil {
		//: already there.
		return cur[i]
	}
	next := make([]*laneState, max(len(cur), i+1))
	copy(next, cur)
	st := newLaneState(a, i)
	next[i] = st
	a.lanes.Store(&next)
	//: published.
	return st
}

// swallowErr intentionally discards a non-actionable error: the adapter has
// no caller to return a refused Close to.
func swallowErr(err error) {
	//: nothing to do with it.
	if err == nil {
		//: nothing to discard.
		return
	}
}
