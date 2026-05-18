// Package logger — declares the Sink port — the transport-side
// boundary of the logger architecture. A Sink receives a fully formatted
// byte payload (typically produced by an Encoder) plus the originating
// RecordEvent for sinks that need structured access (CloudWatch metadata,
// S3 object tags, syslog severity mapping).
//
// Concrete Sink implementations live in internal/service/logger/sink/<x>/
// (console, file, multi, async, route, failover, sample, recover, syslog,
// …). Encoders live in internal/service/logger/encoder/. The Handler
// composes one Encoder with one Sink — see commit 7 for the genericHandler
// that wires them together.
package logger

import "context"

// Sink is the transport port consumed by the generic Handler. Implementations
// MUST be safe for concurrent use by multiple goroutines and SHOULD assume
// their own concurrency model (mutex / lock-free / batched flush) so the
// Handler can stay encoder-agnostic. Sinks that need the severity level read
// it directly from r.Level — the port is intentionally kept narrow.
type Sink interface {
	// Write delivers the formatted byte payload p plus the originating record
	// to the underlying transport. Sinks MAY ignore p and re-serialise from
	// r when their wire protocol differs from the encoder's output (e.g. a
	// CloudWatch sink reads r.Time directly to populate the AWS request).
	Write(ctx context.Context, r RecordEvent, p []byte) (n int, err error)
	// Flush forces any buffered records out. A no-op for synchronous sinks.
	Flush(ctx context.Context) (err error)
	// Close releases any resources held by the sink (file descriptors, AWS
	// clients, network connections). Sinks MAY refuse subsequent Writes
	// after Close returns; callers MUST NOT use the sink after closing it.
	Close() (err error)
}
