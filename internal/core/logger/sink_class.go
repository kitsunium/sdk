// Package logger — SinkClass classification for routing and middleware
// composition hints. Read once at sink construction; never on the hot path.
package logger

import "strconv"

// SinkClass tags a sink's operational profile so middleware can refuse to
// stack a redundant decorator on top of it (e.g. Async over a
// RemoteStreaming sink that already owns flow control). Bounded enum —
// adding a class is a deliberate API change, not a string blob.
//
// The zero value is UnknownClass: a sink that forgets to set its class
// fails closed (every class-aware middleware will warn) instead of
// silently passing as LocalClass.
type SinkClass uint8

// Bounded enumeration of sink operational profiles + supporting radix
// constant. Order is part of the API contract — appending new values is
// allowed; reordering is not.
const (
	// UnknownClass is the zero value. A sink reporting UnknownClass is
	// treated as "class metadata missing" by middlewares; they emit a
	// warning per construction inviting the implementer to declare a
	// concrete class.
	UnknownClass SinkClass = iota

	// LocalClass — synchronous, in-process, ~µs latency (console, file
	// with EveryRecord fsync, syslog over a local UNIX socket).
	// Backpressure / circuit-breaker middleware is overkill here.
	LocalClass

	// AsyncLocalClass — local transport behind an in-process ring/queue
	// (file with periodic fsync, async-wrapped console). Bounded loss on
	// SIGKILL is the contract.
	AsyncLocalClass

	// RemoteBatchedClass — coalesces N records into a single network
	// round-trip (CloudWatch PutLogEvents, S3 PutObject, Loki push).
	// Sinks in this class MUST also implement BatchSink for the
	// genericHandler fan-in to skip N×Write.
	RemoteBatchedClass

	// RemoteStreamingClass — long-lived bidirectional stream (OTLP gRPC,
	// syslog TCP w/ TLS). Has its own internal backpressure; wrapping in
	// Async is redundant and double-buffers.
	RemoteStreamingClass

	// maxKnownSinkClass is the inclusive upper bound of declared class
	// values. NewBaseSink uses it to reject out-of-range arguments at
	// construction. Kept inside the same const block so reordering an
	// enum value automatically reorders this guard.
	maxKnownSinkClass = RemoteStreamingClass
)

// String returns the human-readable name of c for diagnostics and audit
// hook messages. Unknown values render as "unknown(N)" so a future class
// reaching production via a stale binary remains identifiable.
func (c SinkClass) String() string {
	//: dense switch — N is small, fixed, and any value below the max is a
	//: documented identity.
	switch c {
	//: zero-value branch — sink forgot to set its class.
	case UnknownClass:
		//: explicit name keeps the missing-metadata warning identifiable
		//: in audit hook output.
		return "unknown"
	//: synchronous local transport branch.
	case LocalClass:
		//: no decorator should add a queue over a synchronous local sink.
		return "local"
	//: in-process queued local transport branch.
	case AsyncLocalClass:
		//: documented bounded loss is the contract.
		return "async-local"
	//: batched remote transport branch.
	case RemoteBatchedClass:
		//: sinks in this class SHOULD also implement BatchSink.
		return "remote-batched"
	//: long-lived bidirectional stream branch.
	case RemoteStreamingClass:
		//: stream owns its own flow control; Async is redundant.
		return "remote-streaming"
	//: forward-compatible branch for values added after this build.
	default:
		//: render the numeric code via stdlib strconv.Itoa (decimal radix
		//: by definition) so a stale binary still reports something a
		//: maintainer can grep, without a magic base argument.
		return "unknown(" + strconv.Itoa(int(c)) + ")"
	}
}
