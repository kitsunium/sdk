// Package metrics — the OTLP payload tree: a Go mirror of
// opentelemetry/proto/{collector/metrics,metrics}/v1, restricted to the fields
// this SDK produces. The common/v1 and resource/v1 messages it embeds —
// KeyValue, AnyValue, Resource, InstrumentationScope — and the proto3-JSON
// scalars are the ones both signals share, in internal/service/observe/internal/otlp.
//
// Field ORDER inside each struct is the schema's FIELD-NUMBER order, not a
// reading order: encoding/json emits struct fields as declared, and deriving
// the order from the document is what makes the expected bytes in the tests
// checkable against the .proto field by field. The visible evidence that the
// order came from the schema rather than from taste is otlpNumberDataPoint,
// which puts attributes AFTER the value because it is field 7 — it replaced a
// long-removed labels field at 1.
//
// Every field this SDK does not produce is ABSENT rather than always-empty
// (rule 5): schemaUrl, unit, flags, exemplars, droppedAttributesCount, and a
// histogram point's min/max. `description` joined the produced set in ADR 0067
// and is omitted only when the metric carries none — see otlpMetric.
package metrics

import "github.com/kitsunium/sdk/internal/service/observe/internal/otlp"

// otlpRequest is ExportMetricsServiceRequest — the body of a POST to
// /v1/metrics. Exactly one resourceMetrics entry, because one Meter has exactly
// one Resource.
type otlpRequest struct {
	ResourceMetrics []otlpResourceMetrics `json:"resourceMetrics"`
}

// otlpResourceMetrics is ResourceMetrics: resource (1) + scopeMetrics (2).
// schemaUrl (3) is absent for the reason ADR 0044 §Decision 4 gives — it is
// optional, nothing here produces one, and an always-empty field is a
// placeholder.
type otlpResourceMetrics struct {
	Resource     otlp.ResourceMessage `json:"resource"`
	ScopeMetrics []otlpScopeMetrics   `json:"scopeMetrics"`
}

// otlpScopeMetrics is ScopeMetrics: scope (1) + metrics (2), with the same
// schemaUrl omission as otlpResourceMetrics.
type otlpScopeMetrics struct {
	Scope   otlp.ScopeMessage `json:"scope"`
	Metrics []otlpMetric      `json:"metrics,omitempty"`
}

// otlpMetric is Metric: name (1) + description (2), plus the data oneof —
// gauge (5), sum (7), histogram (9). Exactly one of the three pointers is
// non-nil, which is how a oneof is spelled in Go.
//
// description is `string description = 2;` in the schema — a PLAIN proto3
// string, with no `optional` and therefore NO explicit presence. "" and absent
// are the same value to a receiver, so this is the opposite case from the three
// fields ADR 0048 emits at their zero: `asInt`/`asDouble` are oneof members and
// a histogram point's `sum` is `optional double`, both of which HAVE presence,
// and `isMonotonic` is emitted because false is its surprising answer. An empty
// description has no surprising answer — it means nobody wrote one — so it is
// omitted, which is also what the proto3-JSON default mapping does with a
// default-valued field.
//
// unit (3) is still absent: a Meter records none, and an empty string for it
// would be a placeholder (rule 5).
type otlpMetric struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Gauge       *otlpGauge     `json:"gauge,omitempty"`
	Sum         *otlpSum       `json:"sum,omitempty"`
	Histogram   *otlpHistogram `json:"histogram,omitempty"`
}

// otlpGauge is Gauge: dataPoints (1) and nothing else. There is no
// aggregationTemporality field on this message, in the schema or in this SDK's
// model, because a sampled reading covers no window.
type otlpGauge struct {
	DataPoints []otlpNumberDataPoint `json:"dataPoints,omitempty"`
}

// otlpSum is Sum: dataPoints (1) + aggregationTemporality (2) + isMonotonic (3).
//
// isMonotonic is ALWAYS emitted, although proto3 JSON would omit a false bool.
// It is the one field that tells a Counter from an UpDownCounter, and omitting
// it would make the field disappear in exactly the case that carries the
// surprising answer. A receiver reads the same value either way; a human
// reading the payload, and the test that pins these bytes, do not.
type otlpSum struct {
	DataPoints             []otlpNumberDataPoint `json:"dataPoints,omitempty"`
	AggregationTemporality int                   `json:"aggregationTemporality"`
	IsMonotonic            bool                  `json:"isMonotonic"`
}

// otlpHistogram is Histogram: dataPoints (1) + aggregationTemporality (2).
// Exponential histograms are a separate message this SDK does not produce
// (ADR 0044 §Deferred).
type otlpHistogram struct {
	DataPoints             []otlpHistogramDataPoint `json:"dataPoints,omitempty"`
	AggregationTemporality int                      `json:"aggregationTemporality"`
}

// otlpNumberDataPoint is NumberDataPoint — the point shape a Sum and a Gauge
// share: startTimeUnixNano (2), timeUnixNano (3), asDouble (4), asInt (6),
// attributes (7).
//
// The two value fields are POINTERS because they are oneof members, which have
// explicit presence: an omitted oneof means "no case selected", not "the
// default", so a counter sitting at 0 must still emit asInt.
//
// exemplars (5) and flags (8) are absent: this SDK produces no exemplar
// (ADR 0044 §Deferred — no tracing domain, so no span id to carry) and sets no
// data-point flag.
type otlpNumberDataPoint struct {
	StartTimeUnixNano otlp.Uint64     `json:"startTimeUnixNano"`
	TimeUnixNano      otlp.Uint64     `json:"timeUnixNano"`
	AsDouble          *otlp.Double    `json:"asDouble,omitempty"`
	AsInt             *otlp.Int64     `json:"asInt,omitempty"`
	Attributes        []otlp.KeyValue `json:"attributes,omitempty"`
}

// otlpHistogramDataPoint is HistogramDataPoint, again in field-number order:
// startTimeUnixNano (2), timeUnixNano (3), count (4), sum (5), bucketCounts (6),
// explicitBounds (7), attributes (9).
//
// sum is a POINTER because the schema declares it `optional double`: it has
// explicit presence, so an emitted 0 means "the observations summed to zero"
// and an omitted field means "no sum was recorded". This SDK always has one, so
// it is always emitted — including when it is 0, which omitempty would have
// swallowed.
//
// min (11) and max (12) are absent: the meter tracks neither, and both are
// optional. exemplars (8) and flags (10) are absent for the reasons
// otlpNumberDataPoint gives.
type otlpHistogramDataPoint struct {
	StartTimeUnixNano otlp.Uint64     `json:"startTimeUnixNano"`
	TimeUnixNano      otlp.Uint64     `json:"timeUnixNano"`
	Count             otlp.Uint64     `json:"count"`
	Sum               *otlp.Double    `json:"sum"`
	BucketCounts      []otlp.Uint64   `json:"bucketCounts,omitempty"`
	ExplicitBounds    []otlp.Double   `json:"explicitBounds,omitempty"`
	Attributes        []otlp.KeyValue `json:"attributes,omitempty"`
}
