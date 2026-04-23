// Package logger: handler.go declares genericHandler — the corelogger.Handler
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
}

// NewHandler composes enc with sink and gates emission on the supplied
// minimum level. A nil enc OR sink is rejected so callers cannot
// accidentally construct a dead handler.
//
// Params:
//   - enc: encoder converting records to bytes; nil rejects the call.
//   - sink: transport receiving the encoder's bytes; nil rejects the call.
//   - min: minimum level to emit.
//
// Returns:
//   - corelogger.Handler: a ready-to-use Handler, or nil on rejection.
//   - error: EncoderNil / SinkRequired when enc or sink is nil; nil on success.
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
	//: hand back the configured handler behind the public Handler interface.
	return &genericHandler{enc: enc, sink: sink, min: min}, nil
}

// Enabled reports whether the handler would emit a record at r.Level, taking
// context cancellation into account.
//
// Params:
//   - ctx: request-scoped context; cancelled contexts short-circuit to false.
//   - r: candidate record.
//
// Returns:
//   - enabled: true when r.Level is at or above the configured minimum.
func (h *genericHandler) Enabled(ctx context.Context, r corelogger.RecordEvent) (enabled bool) {
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
//
// Params:
//   - ctx: request-scoped context forwarded to the sink.
//   - r: record to encode and emit.
//
// Returns:
//   - err: ctx.Err() if cancelled (wrapped); otherwise the sink's error.
func (h *genericHandler) Handle(ctx context.Context, r corelogger.RecordEvent) (err error) {
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
//
// Params:
//   - attrs: attributes to prepend on each emitted RecordEvent.
//
// Returns:
//   - child: a new handler carrying the combined attrs.
func (h *genericHandler) WithAttrs(attrs []corelogger.AttrValue) (child corelogger.Handler) {
	//: copy-on-write — child must not alias the parent's attrs slice.
	cp := mergeAttrs(h.attrs, attrs)
	//: share encoder/sink/min/groups, own a private attrs slice.
	return &genericHandler{enc: h.enc, sink: h.sink, attrs: cp, groups: h.groups, min: h.min}
}

// WithGroup returns a derived genericHandler with the supplied group name
// appended onto the active group prefix stack.
//
// Params:
//   - name: group prefix; empty value yields the receiver unchanged.
//
// Returns:
//   - child: a new handler carrying the appended group.
func (h *genericHandler) WithGroup(name string) (child corelogger.Handler) {
	//: empty group is a documented no-op so callers can pass user input.
	if name == "" {
		//: return the receiver unchanged — no extra wrapping.
		return h
	}
	//: copy-on-write — child must not alias the parent's groups slice.
	cp := make([]string, len(h.groups)+1)
	copy(cp, h.groups)
	cp[len(h.groups)] = name
	//: share encoder/sink/min/attrs, own a private groups slice.
	return &genericHandler{enc: h.enc, sink: h.sink, attrs: h.attrs, groups: cp, min: h.min}
}

// mergeAttrs returns a fresh slice combining parent and child attributes
// in order (parent first). Extracted so WithAttrs and Handle share a
// single allocation contract.
//
// Params:
//   - parent: handler-bound attribute prefix.
//   - child: attribute slice to append onto the parent prefix.
//
// Returns:
//   - out: a fresh slice owning a copy of both inputs; never nil aliasing.
func mergeAttrs(parent, child []corelogger.AttrValue) (out []corelogger.AttrValue) {
	//: short-circuit when the parent prefix is empty — clone child verbatim.
	if len(parent) == 0 {
		//: defensive copy of child so the caller cannot mutate handler state.
		return slices.Clone(child)
	}
	//: build the combined slice via slices.Concat for one canonical allocation.
	return slices.Concat(parent, child)
}
