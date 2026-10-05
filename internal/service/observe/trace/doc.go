// Package trace — the Tracer's configuration: producing Resource,
// instrumentation Scope, sampling policy, span destination and clock.
//
// Package trace — the OTLP/HTTP emitter: the exporter that POSTs an encoded
// batch to a collector. The transport itself — endpoint refusal, the default
// client and its own pool, the bounded reads, the classification — is the one
// both signals share (internal/service/observe/internal/otlp); what is this signal's is
// the path, the vocabulary every verdict is named in, and the encoder.
//
// Package trace — OTLP/JSON encoder: SpansValue to the bytes an OTLP receiver
// accepts, implemented from the specification with encoding/json.
//
// Package trace — the outbound HTTP middleware.
//
// Package trace — the inbound HTTP middleware.
//
// Package trace — minting the two identifiers.
//
// Package trace — the span an unsampled trace gets.
//
// Package trace — the numeric rendering a refused sampling ratio is reported with.
// The OTLP encoder's own renderings (a double, a decimal-string 64-bit
// integer) are the ones both signals share, in internal/service/observe/internal/otlp.
//
// Package trace — the one member of a collector's answer that is this signal's
// own: the count an ExportTraceServiceResponse reports rejected.
//
// Package trace — the OTLP payload tree: a Go mirror of
// opentelemetry/proto/{collector/trace,trace}/v1, restricted to the fields this
// SDK produces. The common/v1 and resource/v1 messages it embeds — KeyValue,
// AnyValue, Resource, InstrumentationScope — and the proto3-JSON scalars are
// the ones both signals share, in internal/service/observe/internal/otlp.
//
// Field ORDER inside each struct is the schema's FIELD-NUMBER order, not a
// reading order: encoding/json emits struct fields as declared, and deriving the
// order from the document is what makes the expected bytes in the tests
// checkable against the .proto field by field.
//
// The visible evidence that the order came from the schema rather than from
// taste is otlpSpan, which puts `flags` LAST — after `status` — because it is
// field 16 and status is 15, even though every .proto listing shows `flags`
// beside `parent_span_id` where it reads naturally. The same tell exists in the
// metrics mirror, where `attributes` sits after the value because it is field 7.
//
// Every field this SDK does not produce is ABSENT rather than always-empty
// (rule 5): schemaUrl, droppedAttributesCount, droppedEventsCount and
// droppedLinksCount here, and a scope's attributes in the shared Scope.
//
// Package trace — the OTLP/HTTP exporter's configuration.
//
// Package trace — propagation across a process boundary: the two W3C headers
// written into, and read out of, a coretrace.Carrier. The port and the span
// context are internal/core/observe/trace's; reading and writing the headers
// is this engine's mechanism (ADR 0160 §4).
//
// Package trace — recording an error on a span.
//
// Package trace — the in-memory span destination.
//
// Package trace — the in-memory recorder's configuration.
//
// Package trace — the three samplers, and the fraction that is refused.
//
// Package trace — the live, recording span.
//
// Package trace — the traceparent header: parsing it, and writing it back. The
// span context it carries is a value of internal/core/observe/trace; reading
// and writing the header is this engine's mechanism (ADR 0160 §4).
//
// Package trace is the concrete tracing implementation behind
// internal/core/observe/trace: a Tracer, three samplers, an in-memory recorder, the
// OTLP/JSON encoder and the OTLP/HTTP emitter — all from the OpenTelemetry and
// W3C specifications, importing nothing from go.opentelemetry.io. See ADR 0051.
//
// Package trace — the tracestate header read into a list. The list and the
// grammar of its members are internal/core/observe/trace's StateValue; reading
// the header — its commas, its optional whitespace, its "=" — is this engine's
// mechanism (ADR 0160 §4).
package trace
