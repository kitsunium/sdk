// Package logger: logger.go wires a core.Handler into a core.Logger. It
// provides the thin loggerImpl that routes Log calls through the Handler's
// Enabled fast-path and honours With-derived attrs via copy-on-write.
package logger

import (
	"context"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

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
	//: build the RecordEvent once so we can pass it to Enabled and Handle.
	r := corelogger.RecordEvent{Level: lv, Message: msg, Attrs: attrs}
	//: short-circuit when the handler reports disabled — avoids formatting work.
	if !l.h.Enabled(ctx, r) {
		//: nothing to emit at this level.
		return
	}
	//: loggers must never panic from Log; swallowing the error is intentional.
	l.h.Handle(ctx, r)
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
