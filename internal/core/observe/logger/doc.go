// Package logger — range 0.3.1.*, the logger engine's (ADR 0005
// service/observe/logger block), declared here since ADR 0160: the engine,
// internal/service/observe/logger, returns these and declares none.
//
// Package logger — declares the Encoder port — the format-side
// boundary that mirrors Sink (transport-side). Both ports live in core/
// per hexagonal architecture conventions; concrete adapters live under
// internal/service/observe/logger/encoder/. See ADR 0005.
//
// Package logger — declares the sentinels the logger engine
// (internal/service/observe/logger) returns from its constructors and Handle
// methods. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
//
// Package logger — declares the Handler interface that concrete
// implementations in sdk/internal/service satisfy. Handlers format
// RecordEvent values and write them to the backing sink.
//
// Package logger — declares the Kind enum that discriminates the union
// payload carried by Value. Handlers switch on Kind to select the matching
// accessor (String, Int64, Float64, …) instead of paying the cost of an
// `any` type assertion at every render call.
//
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
//
// Package logger — declares the trace-correlation half of a log record: the
// TraceContextValue a RecordEvent carries, and the TraceContextSource port
// that reads one off a context.Context.
//
// The pair lives here rather than in internal/core/observe/trace because a log record
// is not a span: it borrows two identifiers from one. Keeping the value local
// keeps this package stdlib-only — importing the trace domain would put its
// whole model (and the shared core/observe/otel model behind it) in front of every
// consumer that only wants a line on stderr. The binding between the two lives at the top
// layer, in pkg/v1/observe/logger, which is allowed to know both domains. See
// ADR 0062.
//
// Package logger — declares the Value type — a discriminated union
// carrying the payload of an AttrValue without forcing every concrete type
// through `any`. Handlers switch on Value.Kind() to select a typed accessor
// (Int64, Float64, String, …) and skip the cost of reflection at format time.
//
// Storage layout: bool / int64 / uint64 / float64 / time.Duration share the
// `num` field via bit-packing (math.Float64bits for floats, two's complement
// for ints). Strings live in `str`. Time, Group and Any payloads live in
// `any`. The zero Value carries Kind == KindAny with a nil `any` payload.
package logger
