// Package recover wraps a downstream Sink with panic recovery so a buggy
// downstream cannot bring down the producer's goroutine. Recovered panics
// surface as Panicked errors so the caller can react without losing the
// stack trace context.
//
// Use case: defensive guard around third-party sinks (HTTP clients, AWS
// SDKs, custom user code) where a panic in Write would otherwise unwind
// the application's hot path.
package recover

import (
	"context"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// recoverSink wraps a downstream sink with panic recovery on every method.
type recoverSink struct {
	// downstream is the wrapped sink whose panics are caught.
	downstream corelogger.Sink
}

// New wraps downstream with panic recovery. Panics in Write / Flush /
// Close are converted into Panicked sentinels so the caller stays alive.
//
// Params:
//   - downstream: the wrapped sink whose panics are caught; nil rejects.
//
// Returns:
//   - corelogger.Sink: the recover Sink behind the public interface.
//   - error: DownstreamNil when downstream is nil.
func New(downstream corelogger.Sink) (sink corelogger.Sink, err error) {
	//: refuse a nil downstream — there would be nothing to wrap.
	if downstream == nil {
		//: documented sentinel — caller must supply a downstream sink.
		return nil, DownstreamNil
	}
	//: hand back the recover Sink behind the public interface.
	return &recoverSink{downstream: downstream}, nil
}

// Write forwards p to the downstream sink under panic recovery.
//
// Params:
//   - ctx: request-scoped context forwarded to the downstream sink.
//   - rec: originating record forwarded to the downstream sink.
//   - p: formatted bytes forwarded to the downstream sink.
//
// Returns:
//   - n: bytes accepted by the downstream sink (0 on panic).
//   - err: downstream's error or Panicked wrapping the recovered value.
func (s *recoverSink) Write(ctx context.Context, rec corelogger.RecordEvent, p []byte) (n int, err error) {
	//: defer the recovery so any panic in the downstream call lands here.
	defer func() {
		//: capture the recover() return value as a Panicked error.
		if rv := recover(); rv != nil {
			//: rich rendering stays in Private (log-only); Source() chain
			//: carries only the type via panicValue.Error() to prevent leak.
			err = errs.Wrap(panicValue{v: rv}, errs.WrapParams{
				Code:    CodeRecoverPanicked,
				Reason:  "RECOVER_PANICKED",
				Public:  "Wrapped sink panicked",
				Private: "service/logger/middleware/recover.Write caught panic: " + safeString(rv),
			}, errs.Int("level", int(rec.Level)), errs.String("panic_type", safeTypeName(rv)))
		}
	}()
	//: delegate to the downstream sink — its panic (if any) is caught above.
	return s.downstream.Write(ctx, rec, p)
}

// Flush forwards to the downstream sink under panic recovery.
//
// Params:
//   - ctx: request-scoped context forwarded to the downstream sink.
//
// Returns:
//   - err: downstream's error or Panicked wrapping the recovered value.
func (s *recoverSink) Flush(ctx context.Context) (err error) {
	//: defer the recovery so any panic in the downstream call lands here.
	defer func() {
		//: capture the recover() return value as a Panicked error.
		if rv := recover(); rv != nil {
			//: rich rendering stays in Private (log-only); Source() chain
			//: carries only the type via panicValue.Error() to prevent leak.
			err = errs.Wrap(panicValue{v: rv}, errs.WrapParams{
				Code:    CodeRecoverPanicked,
				Reason:  "RECOVER_PANICKED",
				Public:  "Wrapped sink panicked",
				Private: "service/logger/middleware/recover.Flush caught panic: " + safeString(rv),
			}, errs.String("panic_type", safeTypeName(rv)))
		}
	}()
	//: delegate to the downstream sink — its panic (if any) is caught above.
	return s.downstream.Flush(ctx)
}

// Close forwards to the downstream sink under panic recovery.
//
// Returns:
//   - err: downstream's error or Panicked wrapping the recovered value.
func (s *recoverSink) Close() (err error) {
	//: defer the recovery so any panic in the downstream call lands here.
	defer func() {
		//: capture the recover() return value as a Panicked error.
		if rv := recover(); rv != nil {
			//: rich rendering stays in Private (log-only); Source() chain
			//: carries only the type via panicValue.Error() to prevent leak.
			err = errs.Wrap(panicValue{v: rv}, errs.WrapParams{
				Code:    CodeRecoverPanicked,
				Reason:  "RECOVER_PANICKED",
				Public:  "Wrapped sink panicked",
				Private: "service/logger/middleware/recover.Close caught panic: " + safeString(rv),
			}, errs.String("panic_type", safeTypeName(rv)))
		}
	}()
	//: delegate to the downstream sink — its panic (if any) is caught above.
	return s.downstream.Close()
}
