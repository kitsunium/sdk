// Package logger — declares genericHandler — the corelogger.Handler
// implementation that composes an Encoder (format) with a Sink (transport).
// It replaces the legacy TextHandler that fused both responsibilities into
// one struct, and ships as the single Handler the service layer offers.
package logger

import (
	"context"
	"slices"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/buffer"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/encoder"
)

// genericHandler is the default corelogger.Handler implementation. It runs
// records through the configured Encoder and forwards the resulting bytes
// to the configured Sink.
type genericHandler struct {
	// enc renders RecordEvent payloads into bytes.
	enc encoder.Encoder
	// sink delivers the encoder's bytes to the underlying transport.
	sink corelogger.Sink
	// attrs are prepended to every RecordEvent (handler-bound With*).
	attrs []corelogger.AttrValue
	// groups is the active group prefix stack accumulated via WithGroup.
	groups []string
	// min is the minimum level the handler emits; records below are dropped.
	min level.Level
	// clk sources the timestamp when RecordEvent.Time is the zero value. The
	// handler owns the Time fill so the encoded line and any downstream sink
	// share one coherent instant (V25).
	clk clock.Clock
}

// NewHandler composes enc with sink and gates emission on the supplied
// minimum level. A nil enc OR sink is rejected so callers cannot
// accidentally construct a dead handler.
func NewHandler(enc encoder.Encoder, sink corelogger.Sink, min level.Level) (h corelogger.Handler, err error) {
	//: reject nil encoder so the format step never panics on the hot path.
	if enc == nil {
		//: caller supplied no encoder — return the documented sentinel.
		return nil, EncoderNil
	}
	//: reject nil sink so the transport step never panics on the hot path.
	if sink == nil {
		//: caller supplied no sink — return the documented sentinel.
		return nil, SinkRequired
	}
	//: hand back the configured handler bound to the real wall clock.
	return &genericHandler{enc: enc, sink: sink, min: min, clk: clock.System}, nil
}

// Enabled reports whether the handler would emit a record at r.Level, taking
// context cancellation into account.
func (h *genericHandler) Enabled(ctx context.Context, r corelogger.RecordEvent) bool {
	//: honour context cancellation: cancelled contexts short-circuit to disabled.
	if ctx != nil && ctx.Err() != nil {
		//: caller's context is already done; skip emission entirely.
		return false
	}
	//: threshold check; handlers below min are expected to skip formatting.
	return r.Level >= h.min
}

// Handle encodes r through the configured encoder and forwards the bytes to
// the configured sink. Borrows a scratch buffer from kernel/buffer so the
// hot path stays allocation-free.
func (h *genericHandler) Handle(ctx context.Context, r corelogger.RecordEvent) error {
	//: honour context cancellation early so the encoder step never runs.
	if ctx != nil && ctx.Err() != nil {
		//: wrap ctx.Err() so consumers get both our reason and stdlib Is().
		return errs.Wrap(ctx.Err(), errs.WrapParams{
			Code:    CodeCtxCancelled,
			Reason:  "CTX_CANCELLED",
			Public:  "Logging aborted due to cancellation",
			Private: "service/logger.genericHandler.Handle saw a cancelled context",
		})
	}
	//: stamp Time once at the handler boundary when the caller left it zero so
	//: the encoded line and any downstream sink observe one coherent instant
	//: (V25). Sinks MUST NOT restamp a non-zero Time.
	if r.Time.IsZero() {
		//: source the instant from the injected clock so tests can freeze it.
		r.Time = h.clk.Now()
	}
	//: borrow a buffer so the encoder writes into a recycled scratchpad.
	bp := buffer.Get()
	defer buffer.Put(bp)
	//: prepend handler-bound attrs onto the record before encoding.
	bound := mergeAttrs(h.attrs, r.Attrs)
	r.Attrs = bound
	//: format the record into the borrowed buffer.
	line := h.enc.Append(*bp, h.groups, r)
	*bp = line
	//: hand the encoded bytes to the sink for delivery.
	_, werr := h.sink.Write(ctx, r, line)
	//: return the sink's error verbatim (already wrapped by the sink).
	return werr
}

// WithAttrs returns a derived genericHandler with the supplied attributes
// prepended onto every subsequent RecordEvent.
func (h *genericHandler) WithAttrs(attrs []corelogger.AttrValue) corelogger.Handler {
	//: copy-on-write — child must not alias the parent's attrs slice.
	cp := mergeAttrs(h.attrs, attrs)
	//: share encoder/sink/min/groups/clk, own a private attrs slice.
	return &genericHandler{enc: h.enc, sink: h.sink, attrs: cp, groups: h.groups, min: h.min, clk: h.clk}
}

// WithGroup returns a derived genericHandler with the supplied group name
// appended onto the active group prefix stack.
func (h *genericHandler) WithGroup(name string) corelogger.Handler {
	//: empty group is a documented no-op so callers can pass user input.
	if name == "" {
		//: return the receiver unchanged — no extra wrapping.
		return h
	}
	//: copy-on-write — child must not alias the parent's groups slice.
	cp := make([]string, len(h.groups)+1)
	copy(cp, h.groups)
	cp[len(h.groups)] = name
	//: share encoder/sink/min/attrs/clk, own a private groups slice.
	return &genericHandler{enc: h.enc, sink: h.sink, attrs: h.attrs, groups: cp, min: h.min, clk: h.clk}
}

// mergeAttrs returns a fresh slice combining parent and child attributes
// in order (parent first). Extracted so WithAttrs and Handle share a
// single allocation contract.
func mergeAttrs(parent, child []corelogger.AttrValue) []corelogger.AttrValue {
	//: short-circuit when the parent prefix is empty — clone child verbatim.
	if len(parent) == 0 {
		//: defensive copy of child so the caller cannot mutate handler state.
		return slices.Clone(child)
	}
	//: build the combined slice via slices.Concat for one canonical allocation.
	return slices.Concat(parent, child)
}
