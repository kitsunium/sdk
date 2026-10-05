// Package recover — holds the Config struct consumed by NewWithConfig.
// Pulled into its own file so recover_sink.go stays focused on the Sink
// contract.
//
// Package recover — declares the panicValue adapter that
// converts a recover() return value (any) into the error interface so
// errs.Wrap can carry it without losing the original Stringer behaviour.
//
// Package recover wraps a downstream Sink with panic recovery so a buggy
// downstream cannot bring down the producer's goroutine. Recovered panics
// surface as Panicked errors so the caller can react without losing the
// stack trace context.
//
// Use case: defensive guard around third-party sinks (HTTP clients, AWS
// SDKs, custom user code) where a panic in Write would otherwise unwind
// the application's hot path.
package recover
