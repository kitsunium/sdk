// Package levelgate provides a Sink decorator that forwards records at or above
// a minimum severity and silently drops the rest. It implements the per-writer
// MinLevel floor for the writer factories (console, file, s3, cloudwatch)
// without the route middleware's NoMatch error — a below-threshold record is a
// successful no-op, not a failure, so it never pollutes a multi fan-out.
package levelgate

import (
	"context"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// gateSink forwards Write to inner only when the record meets the floor.
type gateSink struct {
	// inner is the wrapped terminal/middleware sink.
	inner corelogger.Sink
	// min is the inclusive severity floor; records below it are dropped.
	min level.Level
}

// New wraps inner so only records with Level >= min reach it. When min equals
// level.Info — the zero value of a writer config's MinLevel — New returns inner
// unwrapped: the writer then inherits the handler-global level with no extra
// gate and no per-record overhead.
//
// IFACE-PLUGIN: the gate is returned behind the Sink interface; the concrete
// gateSink type stays unexported.
func New(inner corelogger.Sink, min level.Level) corelogger.Sink {
	//: Info is the config zero value — treat it as "inherit", skip the wrapper.
	if min == level.Info {
		//: hand back the sink untouched so the hot path stays flat.
		return inner
	}
	//: a real floor was requested — install the gate.
	return &gateSink{inner: inner, min: min}
}

// Write forwards records at or above the floor and silently drops the rest,
// reporting a successful (len(p), nil) for drops so fan-out callers never see a
// spurious failure.
func (s *gateSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: below-floor records are dropped as a successful no-op.
	if r.Level < s.min {
		//: report the payload as accepted so multi never aggregates a failure.
		return len(p), nil
	}
	//: at or above the floor — delegate to the wrapped sink.
	return s.inner.Write(ctx, r, p)
}

// Flush delegates to the wrapped sink.
func (s *gateSink) Flush(ctx context.Context) error {
	//: the gate buffers nothing; forward the flush verbatim.
	return s.inner.Flush(ctx)
}

// Close delegates to the wrapped sink.
func (s *gateSink) Close() error {
	//: the gate owns no resources; forward the close verbatim.
	return s.inner.Close()
}
