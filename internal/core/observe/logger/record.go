// Package logger — declares the Logger interface — the primary entry
// point consumers interact with. A Logger binds a Handler and exposes
// ergonomic Log / With / Enabled operations over it.
//
// Package logger — defines the RecordEvent value carried between
// Logger and Handler. It is an immutable snapshot of a single log event at
// the core boundary.
//
// Package logger — declares the Sink port — the transport-side
// boundary of the logger architecture. A Sink receives a fully formatted
// byte payload (typically produced by an Encoder) plus the originating
// RecordEvent for sinks that need structured access (CloudWatch metadata,
// S3 object tags, syslog severity mapping).
//
// Concrete Sink implementations live in internal/service/observe/logger/sink/<x>/
// (console, file, multi, async, route, failover, sample, recover, syslog,
// …). Encoders live in internal/service/observe/logger/encoder/. The Handler
// composes one Encoder with one Sink — see commit 7 for the genericHandler
// that wires them together.
package logger

import (
	"time"

	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
)

// RecordEvent captures a single log event; handlers are responsible for writing
// or forwarding it. Instances are intended to be used read-only after
// construction by the Logger implementation.
type RecordEvent struct {
	// Time is the instant the event occurred; zero means "fill in at handle time".
	Time time.Time
	// Level is the severity of the event.
	Level level.Level
	// Message is the human-readable description of the event.
	Message string
	// PC is the program counter captured at the Log call site (via
	// runtime.Callers). Zero means "no caller information available";
	// handlers SHOULD resolve PC into a frame lazily at format time.
	PC uintptr
	// Attrs carries the structured key/value pairs attached to this event.
	Attrs []AttrValue
	// TraceContext is the identity of the span this event was emitted
	// inside; the zero value means "no trace here" and MUST render nothing.
	// It is a FIELD rather than two entries in Attrs because the Encoder
	// port receives no context.Context — the identity has to travel on the
	// record to reach the formatter at all — and because OpenTelemetry
	// prescribes trace_id / span_id as TOP-LEVEL keys of the log object,
	// which an attribute cannot be: WithGroup would prefix it. See ADR 0062.
	TraceContext TraceContextValue
}

// AttrValue is an immutable key/value pair carried by a RecordEvent. Use the
// typed Value constructors (StringValue, Int64Value, …) to build the Value
// field — the zero Value is a valid KindAny carrying nil.
type AttrValue struct {
	// Key identifies the attribute in the output line.
	Key string
	// Value carries the typed payload; handlers dispatch on Value.Kind().
	Value Value
}
