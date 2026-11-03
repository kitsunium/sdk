package metrics

import "time"

// ExemplarValue is one measurement kept beside a data point as a link into
// the trace that produced it: metrics.proto's Exemplar — the time, the value,
// and the span and trace identifiers as raw arrays, never core/observe/trace's
// types, which this package must not import (ADR 0051 §Deferred).
//
// filtered_attributes is absent rather than always empty: a series is keyed
// by EVERY attribute of its measurement (no view drops any), so an exemplar
// has none the point does not already carry.
type ExemplarValue struct {
	// Time is when the measurement was recorded, on the meter's clock.
	Time time.Time
	// Value is the measurement. On a sum point it holds the integer the
	// instrument was handed — exact below 2^53 — and OTLP/JSON writes it as
	// asInt; on a histogram point it is the recorded double, asDouble.
	Value float64
	// TraceID is the trace the measurement was recorded in.
	TraceID [16]byte
	// SpanID is the span the measurement was recorded in.
	SpanID [8]byte
}
