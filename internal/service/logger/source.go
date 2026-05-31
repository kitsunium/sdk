// Package logger — declares the sourceHandler decorator — a Handler
// middleware that resolves the RecordEvent.PC captured by the front-end into a
// structured "source" attribute (file:line:function) before delegating to the
// wrapped Handler. It is additive and opt-in: the record shape is unchanged
// until a caller wraps a Logger via WithCaller, and resolution is stdlib-only
// (runtime.CallersFrames).
package logger

import (
	"context"
	"runtime"
	"strconv"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// SourceFieldKey is the attribute key under which the resolved caller location
// is emitted. Encoders render it like any other string attribute, so both the
// text and JSON encoders honour it without modification.
const SourceFieldKey string = "source"

// sourceHandler is a corelogger.Handler decorator that resolves the captured
// program counter into a "source" attribute and prepends it to each record
// before delegating to the wrapped Handler. A zero PC (no caller information)
// is passed through untouched so the annotation never appears empty.
type sourceHandler struct {
	// next is the wrapped Handler that performs the real format + transport.
	next corelogger.Handler
	// skip is the extra stack-frame offset reserved for wrapper layers; it is
	// retained for symmetry with the front-end capture and future re-capture
	// paths. Resolution today reads the already-captured RecordEvent.PC.
	skip int
}

// newSourceHandler wraps next so every handled RecordEvent gains a resolved
// "source" attribute. A nil next is rejected with HandlerNil so callers cannot
// build a dead decorator. The skip offset is clamped to a non-negative value.
func newSourceHandler(next corelogger.Handler, skip int) (h corelogger.Handler, err error) {
	//: reject a nil inner handler so Handle never panics on the hot path.
	if next == nil {
		//: reuse the package's documented nil-handler sentinel.
		return nil, HandlerNil
	}
	//: clamp a negative skip so a wrapper miscount never underflows the offset.
	if skip < 0 {
		//: zero is the natural floor — resolve the captured frame as-is.
		skip = 0
	}
	//: hand back the decorator behind the public Handler interface.
	return &sourceHandler{next: next, skip: skip}, nil
}

// Enabled delegates the level decision to the wrapped Handler unchanged.
func (h *sourceHandler) Enabled(ctx context.Context, r corelogger.RecordEvent) bool {
	//: the decorator adds no gating of its own — defer to the inner handler.
	return h.next.Enabled(ctx, r)
}

// Handle resolves r.PC into a "source" attribute, prepends it to the record's
// attributes, and delegates to the wrapped Handler.
func (h *sourceHandler) Handle(ctx context.Context, r corelogger.RecordEvent) error {
	//: attach the resolved source annotation before the inner handler formats.
	r.Attrs = withSource(r.Attrs, r.PC)
	//: delegate the real format + transport to the wrapped handler.
	return h.next.Handle(ctx, r)
}

// WithAttrs threads the bound attrs through the wrapped Handler and rewraps the
// derived Handler so the source annotation survives With-derived loggers.
func (h *sourceHandler) WithAttrs(attrs []corelogger.AttrValue) corelogger.Handler {
	//: rewrap the derived inner handler so source resolution is preserved.
	return &sourceHandler{next: h.next.WithAttrs(attrs), skip: h.skip}
}

// WithGroup threads the group name through the wrapped Handler and rewraps the
// derived Handler so the source annotation survives grouped loggers.
func (h *sourceHandler) WithGroup(name string) corelogger.Handler {
	//: rewrap the derived inner handler so source resolution is preserved.
	return &sourceHandler{next: h.next.WithGroup(name), skip: h.skip}
}

// withSource resolves pc into a "source" attribute and appends it to attrs. An
// unresolvable program counter leaves attrs untouched so a missing frame never
// injects an empty annotation into the record.
func withSource(attrs []corelogger.AttrValue, pc uintptr) []corelogger.AttrValue {
	//: a zero program counter carries no resolvable caller frame.
	if pc == 0 {
		//: nothing to annotate — return the attrs unchanged.
		return attrs
	}
	//: resolve the single captured frame via the stdlib runtime.
	frame, ok := resolveFrame(pc)
	//: skip appending when the runtime had no frame to offer.
	if !ok {
		//: degrade silently — the record keeps its original attrs.
		return attrs
	}
	//: append the rendered location under the well-known source key.
	return append(attrs, corelogger.AttrValue{
		Key:   SourceFieldKey,
		Value: corelogger.StringValue(renderFrame(frame)),
	})
}

// resolveFrame maps a single program counter to its runtime.Frame. The second
// result is false when the runtime cannot map pc to a frame.
func resolveFrame(pc uintptr) (frame runtime.Frame, ok bool) {
	//: ask the runtime for the frame backing this single program counter.
	frame, _ = runtime.CallersFrames([]uintptr{pc}).Next()
	//: an empty function AND file means the runtime had nothing to resolve.
	if frame.Function == "" && frame.File == "" {
		//: report the miss so the caller can skip the annotation.
		return frame, false
	}
	//: hand back the resolved frame.
	return frame, true
}

// renderFrame formats frame as the canonical "file:line:function" string.
func renderFrame(frame runtime.Frame) string {
	//: assemble the canonical file:line:function rendering.
	return frame.File + ":" + strconv.Itoa(frame.Line) + ":" + frame.Function
}

// WithCaller returns a derived Logger whose emitted records carry a "source"
// attribute resolving the captured call-site program counter into
// file:line:function. The skip argument is the extra stack-frame offset
// reserved for wrapper layers; pass 0 for direct callers. Foreign Logger
// implementations (not produced by New) are returned unchanged so callers fail
// safe rather than losing their logger. A nil Logger is returned as-is.
func WithCaller(lg corelogger.Logger, skip int) corelogger.Logger {
	//: a nil logger has nothing to wrap — hand it straight back.
	if lg == nil {
		//: preserve the nil so the caller observes their own input.
		return lg
	}
	//: only loggers built by New expose the concrete handler to decorate.
	impl, ok := lg.(*loggerImpl)
	//: refuse foreign Logger implementations by returning them untouched.
	if !ok {
		//: fail safe — the caller keeps a working, unannotated logger.
		return lg
	}
	//: wrap the inner handler so every record gains the source annotation.
	wrapped, err := newSourceHandler(impl.h, skip)
	//: a wrap failure (impossible for a live loggerImpl) degrades to the input.
	if err != nil {
		//: never strand the caller — return the original logger.
		return lg
	}
	//: hand back a fresh loggerImpl over the source-resolving handler.
	return &loggerImpl{h: wrapped}
}
