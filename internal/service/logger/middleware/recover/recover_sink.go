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
	// onPanic observes every absorbed panic; never nil — noopOnPanic is
	// substituted when Config.OnPanic is unset so the recover block can fire
	// it unconditionally without a nil check.
	onPanic func(err error)
}

// New wraps downstream with panic recovery. Panics in Write / Flush /
// Close are converted into Panicked sentinels so the caller stays alive.
// The absorbed panic is otherwise invisible under a Logger that swallows
// handler errors — wire NewWithConfig's OnPanic hook to observe it.
func New(downstream corelogger.Sink) (sink corelogger.Sink, err error) {
	//: defer to the Config-aware constructor with the zero-value defaults.
	return NewWithConfig(downstream, Config{})
}

// NewWithConfig wraps downstream with panic recovery and the supplied
// Config. A zero-value cfg is equivalent to New: panics in Write / Flush /
// Close become Panicked sentinels and no callback fires. Setting
// cfg.OnPanic makes each absorbed panic observable without changing the
// returned error — see Finding V34.
func NewWithConfig(downstream corelogger.Sink, cfg Config) (sink corelogger.Sink, err error) {
	//: refuse a nil downstream — there would be nothing to wrap.
	if downstream == nil {
		//: documented sentinel — caller must supply a downstream sink.
		return nil, DownstreamNil
	}
	//: substitute the no-op default when the caller did not wire a hook.
	observer := cfg.OnPanic
	//: nil hook degrades to a no-op so the recover block can call it unconditionally.
	if observer == nil {
		//: fall back to the documented no-op.
		observer = noopOnPanic
	}
	//: hand back the recover Sink behind the public interface.
	return &recoverSink{downstream: downstream, onPanic: observer}, nil
}

// noopOnPanic is the documented sink for the OnPanic callback when the
// caller does not supply one. Keeps the recover block's call path branch-free.
func noopOnPanic(err error) {
	//: read the parameter so the unused-param audit treats this no-op as intentional.
	if err == nil {
		//: nothing to observe on the (unreachable) nil path.
		return
	}
}

// Write forwards p to the downstream sink under panic recovery.
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
			//: surface the absorbed panic so a Logger that swallows handler
			//: errors does not render this runtime bug invisible (V34).
			s.onPanic(err)
		}
	}()
	//: delegate to the downstream sink — its panic (if any) is caught above.
	return s.downstream.Write(ctx, rec, p)
}

// Flush forwards to the downstream sink under panic recovery.
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
			//: surface the absorbed panic so a Logger that swallows handler
			//: errors does not render this runtime bug invisible (V34).
			s.onPanic(err)
		}
	}()
	//: delegate to the downstream sink — its panic (if any) is caught above.
	return s.downstream.Flush(ctx)
}

// Close forwards to the downstream sink under panic recovery.
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
			//: surface the absorbed panic so a Logger that swallows handler
			//: errors does not render this runtime bug invisible (V34).
			s.onPanic(err)
		}
	}()
	//: delegate to the downstream sink — its panic (if any) is caught above.
	return s.downstream.Close()
}
