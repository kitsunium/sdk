// Package logger: logger.go wires a core.Handler into a core.Logger. It
// provides the thin loggerImpl that routes Log calls through the Handler's
// Enabled fast-path and honours With-derived attrs via copy-on-write.
package logger

import (
	"context"
	"runtime"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// callerSkipDepth is the number of stack frames to skip when capturing the
// program counter from runtime.Callers: the runtime.Callers frame itself plus
// the loggerImpl.Log frame, so the captured PC points to the application
// caller rather than the logger plumbing.
const callerSkipDepth int = 2

// loggerImpl is the default core.Logger wrapper over a core.Handler. It
// performs no buffering of its own — every Log call delegates to the Handler.
type loggerImpl struct {
	// h is the underlying Handler that receives every emitted RecordEvent.
	h corelogger.Handler
}

// New wraps a core.Handler inside a core.Logger.
//
// Params:
//   - h: handler that will receive every RecordEvent emitted by the Logger.
//
// Returns:
//   - corelogger.Logger: a Logger forwarding to h; nil on rejection.
//   - error: HandlerNil when h is nil; nil on success.
func New(h corelogger.Handler) (lg corelogger.Logger, err error) {
	//: reject nil handlers so callers cannot accidentally construct a dead Logger.
	if h == nil {
		//: caller supplied no handler — return the documented sentinel.
		return nil, HandlerNil
	}
	//: wrap the handler in the concrete loggerImpl.
	return &loggerImpl{h: h}, nil
}

// Enabled delegates to the underlying Handler using a zero RecordEvent whose
// Level field is lv, so callers can gate expensive attribute construction.
//
// Params:
//   - ctx: request-scoped context forwarded to the Handler.
//   - lv: the level being tested.
//
// Returns:
//   - bool: whatever the Handler returns for that level.
func (l *loggerImpl) Enabled(ctx context.Context, lv level.Level) (enabled bool) {
	//: delegate the decision to the Handler using a minimal RecordEvent probe.
	return l.h.Enabled(ctx, corelogger.RecordEvent{Level: lv})
}

// Log builds a RecordEvent at level lv and hands it to the Handler. Records
// below the Handler's threshold are dropped before any formatting happens.
//
// Params:
//   - ctx: request-scoped context forwarded to the Handler.
//   - lv: level of the record.
//   - msg: human-readable message.
//   - attrs: optional attributes attached to the record.
func (l *loggerImpl) Log(ctx context.Context, lv level.Level, msg string, attrs ...corelogger.AttrValue) {
	//: capture the application caller PC for handlers that resolve frames lazily.
	var pcs [1]uintptr
	runtime.Callers(callerSkipDepth, pcs[:])
	//: build the RecordEvent once so we can pass it to Enabled and Handle.
	r := corelogger.RecordEvent{Level: lv, Message: msg, PC: pcs[0], Attrs: attrs}
	//: short-circuit when the handler reports disabled — avoids formatting work.
	if !l.h.Enabled(ctx, r) {
		//: nothing to emit at this level.
		return
	}
	//: route any handler error through the documented swallow helper.
	swallowHandlerError(l.h.Handle(ctx, r))
}

// With returns a derived Logger whose emitted records carry the given attrs.
//
// Params:
//   - attrs: attributes bound to every subsequent RecordEvent.
//
// Returns:
//   - corelogger.Logger: a new Logger sharing the Handler's downstream state.
func (l *loggerImpl) With(attrs ...corelogger.AttrValue) (child corelogger.Logger) {
	//: delegate attr accumulation to the Handler's WithAttrs contract.
	return &loggerImpl{h: l.h.WithAttrs(attrs)}
}

// Build is the package-level entry point that returns a chainable Builder
// bound to lg at the supplied level. It type-asserts lg to the concrete
// loggerImpl created by New; foreign Logger implementations get nil so
// callers detect the misconfiguration immediately.
//
// Params:
//   - lg: a Logger previously built by service/logger.New.
//   - lv: severity level forwarded to the eventual Send.
//
// Returns:
//   - Builder: a recycled chain Builder, or nil when lg is foreign.
func Build(lg corelogger.Logger, lv level.Level) (b Builder) {
	//: only loggers built by service/logger.New carry the Builder pool.
	impl, ok := lg.(*loggerImpl)
	//: refuse foreign Logger implementations so callers fail fast on misuse.
	if !ok {
		//: foreign logger — return nil so callers fail fast.
		return nil
	}
	//: delegate to the concrete impl which owns the recycler.
	return impl.Build(lv)
}

// LogAttrs is the package-level entry point for the slice-overload of Log.
// It mirrors Build by routing through the concrete loggerImpl.
//
// Params:
//   - ctx: request-scoped context forwarded to the Handler.
//   - lg: a Logger previously built by service/logger.New.
//   - lv: level of the record.
//   - msg: human-readable message.
//   - attrs: pre-built attribute slice attached to the record.
func LogAttrs(ctx context.Context, lg corelogger.Logger, lv level.Level, msg string, attrs []corelogger.AttrValue) {
	//: only loggers built by service/logger.New expose the LogAttrs path.
	impl, ok := lg.(*loggerImpl)
	//: foreign Logger silently drops to mirror the Logger.Log contract.
	if !ok {
		//: foreign logger — silently drop, mirroring the Logger contract.
		return
	}
	//: delegate to the concrete impl which owns the runtime.Callers capture.
	impl.LogAttrs(ctx, lv, msg, attrs)
}

// swallowHandlerError is the documented sink for Handler.Handle errors. The
// Logger contract MUST NOT propagate sink failures to the call site (a
// failing log should never crash the caller's business logic), so every
// Log / LogAttrs / Send path routes the handler's error through this
// helper. Future commits may swap this for a configurable OnError callback;
// for now the helper exists so the discard intent is explicit at the call
// site and so a future hook has one canonical interception point.
//
// Params:
//   - err: handler error to discard; non-nil values are intentionally dropped.
func swallowHandlerError(err error) {
	//: explicit early-return so the err parameter is observed by the audit.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
	//: documented drop — Logger contract forbids propagating handler errors.
}

// Build returns a chainable Builder bound to this Logger at the supplied
// level. The Builder is recycled through a sync.Pool so the steady-state
// per-call cost is zero heap allocations once the pool is warm.
//
// Params:
//   - lv: severity level forwarded to the eventual Send.
//
// Returns:
//   - Builder: a recycled chain Builder ready for typed attribute calls.
func (l *loggerImpl) Build(lv level.Level) (b Builder) {
	//: borrow a recycled builder and bind it to this logger + level.
	cb := recordPool.Get()
	cb.owner = l
	cb.lv = lv
	//: hand back behind the public Builder interface.
	return cb
}

// LogAttrs is the slice-overload of Log that avoids the variadic slice
// allocation imposed by Log(... AttrValue). Pre-built attribute slices
// (typically from a caller-managed pool) flow through this entry point
// without the per-call boxing cost.
//
// Params:
//   - ctx: request-scoped context forwarded to the Handler.
//   - lv: level of the record.
//   - msg: human-readable message.
//   - attrs: pre-built attribute slice attached to the record.
func (l *loggerImpl) LogAttrs(ctx context.Context, lv level.Level, msg string, attrs []corelogger.AttrValue) {
	//: capture the application caller PC for handlers that resolve frames lazily.
	var pcs [1]uintptr
	runtime.Callers(callerSkipDepth, pcs[:])
	//: build the RecordEvent once so we can pass it to Enabled and Handle.
	r := corelogger.RecordEvent{Level: lv, Message: msg, PC: pcs[0], Attrs: attrs}
	//: short-circuit when the handler reports disabled — avoids formatting work.
	if !l.h.Enabled(ctx, r) {
		//: nothing to emit at this level.
		return
	}
	//: route any handler error through the documented swallow helper.
	swallowHandlerError(l.h.Handle(ctx, r))
}

// WithGroup returns a derived Logger whose subsequent attributes are
// namespaced under the given group name.
//
// Params:
//   - name: group prefix; empty value yields the receiver unchanged.
//
// Returns:
//   - corelogger.Logger: a new Logger sharing the Handler's downstream state.
func (l *loggerImpl) WithGroup(name string) (child corelogger.Logger) {
	//: empty group is a documented no-op so callers can pass user input.
	if name == "" {
		//: return the receiver unchanged — no extra wrapping.
		return l
	}
	//: delegate group accumulation to the Handler's WithGroup contract.
	return &loggerImpl{h: l.h.WithGroup(name)}
}
