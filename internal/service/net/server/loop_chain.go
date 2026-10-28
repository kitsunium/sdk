package server

import (
	"reflect"
	"slices"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The eight callbacks a loop middleware's layer forwards, in the order the
// probe sends them.
const (
	probeStart int = iota
	probeOpen
	probeData
	probeTimeout
	probeWritable
	probeBatchEnd
	probeClose
	probeHonours
	probeCallbacks
)

// probeNames names each callback in LoopMiddlewareInvalid's "callback" field.
var probeNames = [probeCallbacks]string{
	probeStart: "OnStart", probeOpen: "OnOpen", probeData: "OnData", probeTimeout: "OnTimeout",
	probeWritable: "OnWritable", probeBatchEnd: "OnBatchEnd", probeClose: "OnClose", probeHonours: "Honours",
}

// loopLanes is how many lanes a loop group runs on every backend: Loops,
// zero meaning one (R8). bindLoopGroup has resolved LoopsAuto by then.
func loopLanes(v corenet.LoopValue) int {
	//: zero is one.
	return max(v.Loops, 1)
}

// buildLoopChain wraps h in mws, outermost first, once at Start, and checks
// the chain twice: every layer is a corenet.LoopLayered whose Next is the
// handler it was given, and every callback sent through the same middlewares
// built over a probe reaches the probe. Either failure is
// LoopMiddlewareInvalid, naming the group and the callback — "Next" for the
// first check. With no middleware the chain is h.
func buildLoopChain(group string, h corenet.LoopHandler, mws []corenet.Middleware[corenet.LoopHandler]) (corenet.LoopHandler, error) {
	//: nothing to wrap.
	if len(mws) == 0 {
		//: h serves alone.
		return h, nil
	}
	chain, ok := wrapLoop(h, mws)
	//: a layer that does not say what it wraps, or wraps something else.
	if !ok {
		//: refused by name.
		return nil, invalidLayer(group, "Next")
	}
	probe := &loopProbe{}
	probed, ok := wrapLoop(probe, mws)
	//: the same middlewares, built again over the probe, must hold too.
	if !ok {
		//: refused by name.
		return nil, invalidLayer(group, "Next")
	}
	sendProbe(probed)
	//: every callback must have reached the probe.
	for cb, seen := range probe.seen {
		//: the first one a layer kept.
		if !seen {
			//: refused by name.
			return nil, invalidLayer(group, probeNames[cb])
		}
	}
	//: forwarded throughout.
	return chain, nil
}

// wrapLoop applies mws to h, outermost first, and reports whether every
// layer is a corenet.LoopLayered whose Next is what it wrapped.
func wrapLoop(h corenet.LoopHandler, mws []corenet.Middleware[corenet.LoopHandler]) (corenet.LoopHandler, bool) {
	chain := h
	//: innermost first, so the first listed ends up outermost, as Chain does.
	for _, mw := range slices.Backward(mws) {
		//: a nil entry is skipped, as Chain skips it.
		if mw == nil {
			continue
		}
		layer := mw(chain)
		l, ok := layer.(corenet.LoopLayered)
		//: the layer must name the handler it was given.
		if !ok || !sameHandler(l.Next(), chain) {
			//: broken.
			return nil, false
		}
		chain = layer
	}
	//: every layer wraps the one below it.
	return chain, true
}

// sameHandler reports whether a and b are the same handler. Two values of a
// type that cannot be compared are taken as the same, rather than compared
// into a panic.
func sameHandler(a, b corenet.LoopHandler) bool {
	//: both absent.
	if a == nil || b == nil {
		//: equal only if both are.
		return a == nil && b == nil
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	//: a different type is a different handler.
	if ta != tb {
		//: not the one wrapped.
		return false
	}
	//: a pointer, or any comparable value, compares; the rest cannot.
	return !ta.Comparable() || a == b
}

// chainHonours reports whether a layer of chain, or its handler, honours the
// option of kind k: the chain merges what each of them honours.
func chainHonours(chain corenet.LoopHandler, k corenet.OptionKind) bool {
	//: from the outermost layer to the handler.
	for node := chain; node != nil; {
		//: this layer, or the handler, applies it.
		if h, ok := node.(corenet.LoopOptionHonourer); ok && h.Honours(k) {
			//: honoured.
			return true
		}
		l, ok := node.(corenet.LoopLayered)
		//: the handler: nothing below it.
		if !ok {
			break
		}
		node = l.Next()
	}
	//: nobody applies it.
	return false
}

// invalidLayer is LoopMiddlewareInvalid naming the group and the callback.
func invalidLayer(group, callback string) error {
	//: once, at Start.
	return errs.Wrap(corenet.LoopMiddlewareInvalid, errs.WrapParams{},
		errs.String("group", group), errs.String("callback", callback))
}

// sendProbe sends each of the eight callbacks through probed once, with a
// LoopIO that refuses everything and the zero reference, which no
// connection holds.
func sendProbe(probed corenet.LoopHandler) {
	io := probeIO{}
	var ref corenet.LoopConnRef
	//: OnStart first, as the engine calls it.
	if s, ok := probed.(corenet.LoopStarter); ok {
		s.OnStart(1)
	}
	probed.OnOpen(io, ref)
	probed.OnData(io, ref, nil)
	probed.OnTimeout(io, ref)
	probed.OnWritable(io, ref)
	probed.OnBatchEnd(io)
	probed.OnClose(io, ref, nil)
	//: the option question, which a layer that answers alone also keeps.
	if h, ok := probed.(corenet.LoopOptionHonourer); ok {
		h.Honours(corenet.OptionNone)
	}
}

// loopProbe is the handler a probe chain wraps: it records each callback that
// reaches it.
type loopProbe struct {
	// seen holds, by callback, whether it arrived.
	seen [probeCallbacks]bool
}

// OnStart implements corenet.LoopStarter.
func (p *loopProbe) OnStart(_ int) {
	p.seen[probeStart] = true
}

// OnOpen implements corenet.LoopHandler.
func (p *loopProbe) OnOpen(_ corenet.LoopIO, _ corenet.LoopConnRef) {
	p.seen[probeOpen] = true
}

// OnData implements corenet.LoopHandler.
func (p *loopProbe) OnData(_ corenet.LoopIO, _ corenet.LoopConnRef, buf []byte) int {
	p.seen[probeData] = true
	//: everything, as a handler that reads it all.
	return len(buf)
}

// OnTimeout implements corenet.LoopHandler.
func (p *loopProbe) OnTimeout(_ corenet.LoopIO, _ corenet.LoopConnRef) {
	p.seen[probeTimeout] = true
}

// OnWritable implements corenet.LoopHandler.
func (p *loopProbe) OnWritable(_ corenet.LoopIO, _ corenet.LoopConnRef) {
	p.seen[probeWritable] = true
}

// OnBatchEnd implements corenet.LoopHandler.
func (p *loopProbe) OnBatchEnd(_ corenet.LoopIO) {
	p.seen[probeBatchEnd] = true
}

// OnClose implements corenet.LoopHandler.
func (p *loopProbe) OnClose(_ corenet.LoopIO, _ corenet.LoopConnRef, _ error) {
	p.seen[probeClose] = true
}

// Honours implements corenet.LoopOptionHonourer: the probe honours nothing.
func (p *loopProbe) Honours(_ corenet.OptionKind) bool {
	p.seen[probeHonours] = true
	//: nothing.
	return false
}

// probeIO is the LoopIO of the probe: every connection is stale, the queue is
// closed, and it is lane 0.
type probeIO struct{}

// Write implements corenet.LoopIO.
func (probeIO) Write(_ corenet.LoopConnRef, _ []byte) error {
	//: no connection is live.
	return corenet.LoopStaleRef
}

// Close implements corenet.LoopIO.
func (probeIO) Close(_ corenet.LoopConnRef) error {
	//: no connection is live.
	return corenet.LoopStaleRef
}

// SetDeadline implements corenet.LoopIO.
func (probeIO) SetDeadline(_ corenet.LoopConnRef, _ time.Duration) error {
	//: no connection is live.
	return corenet.LoopStaleRef
}

// Pressure implements corenet.LoopIO.
func (probeIO) Pressure(_ corenet.LoopConnRef) bool {
	//: a stale reference is under none.
	return false
}

// Post implements corenet.LoopIO.
func (probeIO) Post(_ corenet.PostFunc, _ uintptr) error {
	//: the probe runs nothing.
	return corenet.ServerClosed
}

// Loop implements corenet.LoopIO.
func (probeIO) Loop() int {
	//: the one lane the probe is told of.
	return 0
}
