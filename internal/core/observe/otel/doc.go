// Package otel holds the OpenTelemetry types every signal shares: the typed
// attribute (common/v1 KeyValue and AnyValue), the Resource (resource/v1) and
// the InstrumentationScope (common/v1). They live in the SHARED protos because
// every signal uses them, so internal/core/observe/metrics and internal/core/observe/trace both
// sit above this one model rather than one signal borrowing the other's. ADR
// 0051 §Decision 2 named this package and deferred it; it is that extraction.
//
// Like the two signals above it, it implements the specification and imports
// nothing from go.opentelemetry.io — nor anything else of this SDK: it is
// standard library only.
//
// It declares NO error code. An unusable attribute set is a programmer error
// at the call site that wrote it, so the refusal belongs to the signal whose
// API was misused: every function here that can refuse takes the caller's
// sentinel and panics with it. Metrics keeps INVALID_ATTRIBUTE at 0.2.9.4,
// trace has its own at 0.2.20.7, and a value built for one signal is still
// accepted by the other, because there is only one type.
//
// Package otel — Resource: who produced the telemetry.
//
// Package otel — InstrumentationScope: what instrumented the telemetry.
package otel
