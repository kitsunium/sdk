package net

// LoopLayer is the base of a loop middleware's layer: it forwards the eight
// callbacks — OnOpen, OnData, OnTimeout, OnWritable, OnBatchEnd, OnClose,
// Honours and OnStart — to the handler it wraps, and Next returns it. A layer
// embeds it, built by NewLoopLayer, and overrides the callbacks it acts on;
// an override forwards the callback in turn, through the embedded LoopLayer,
// unless the layer answers it itself — a refused connection's bytes, say.
// Honours answers for the wrapped handler, which a LoopOptionHonourer
// answers and any other handler does not; OnStart reaches a LoopStarter and
// stops at any other handler.
type LoopLayer struct {
	// next is the handler the layer wraps.
	next LoopHandler
}

// NewLoopLayer returns the base of a loop middleware's layer over next: the
// LoopLayer a layer embeds, forwarding every callback to next.
func NewLoopLayer(next LoopHandler) LoopLayer {
	//: the one field.
	return LoopLayer{next: next}
}

// Next returns the handler the layer wraps.
//
// IFACE-PLUGIN: the wrapped handler is whatever the next layer, or the
// consumer, wrote.
func (l LoopLayer) Next() LoopHandler {
	//: as built.
	return l.next
}

// OnOpen forwards to the wrapped handler.
func (l LoopLayer) OnOpen(io LoopIO, ref LoopConnRef) {
	l.next.OnOpen(io, ref)
}

// OnData forwards to the wrapped handler and returns what it consumed.
func (l LoopLayer) OnData(io LoopIO, ref LoopConnRef, buf []byte) int {
	//: what the wrapped handler consumed.
	return l.next.OnData(io, ref, buf)
}

// OnTimeout forwards to the wrapped handler.
func (l LoopLayer) OnTimeout(io LoopIO, ref LoopConnRef) {
	l.next.OnTimeout(io, ref)
}

// OnWritable forwards to the wrapped handler.
func (l LoopLayer) OnWritable(io LoopIO, ref LoopConnRef) {
	l.next.OnWritable(io, ref)
}

// OnBatchEnd forwards to the wrapped handler.
func (l LoopLayer) OnBatchEnd(io LoopIO) {
	l.next.OnBatchEnd(io)
}

// OnClose forwards to the wrapped handler.
func (l LoopLayer) OnClose(io LoopIO, ref LoopConnRef, err error) {
	l.next.OnClose(io, ref, err)
}

// Honours reports whether the wrapped handler honours the option of kind k:
// its own Honours when it is a LoopOptionHonourer, false otherwise.
func (l LoopLayer) Honours(k OptionKind) bool {
	h, ok := l.next.(LoopOptionHonourer)
	//: a handler without the capability honours nothing of its own.
	return ok && h.Honours(k)
}

// OnStart forwards to the wrapped handler when it is a LoopStarter.
func (l LoopLayer) OnStart(lanes int) {
	//: a handler that keeps no per-lane state is not told.
	if s, ok := l.next.(LoopStarter); ok {
		s.OnStart(lanes)
	}
}
