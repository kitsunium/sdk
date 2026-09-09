// Package metrics — the OTLP payload tree: a Go mirror of
// opentelemetry/proto/{collector/metrics,metrics,common,resource}/v1, restricted
// to the fields this SDK produces.
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
// (rule 5): schemaUrl, description, unit, flags, exemplars,
// droppedAttributesCount, and a histogram point's min/max.
package metrics

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
	Resource     otlpResource       `json:"resource"`
	ScopeMetrics []otlpScopeMetrics `json:"scopeMetrics"`
}

// otlpResource is Resource: attributes (1). droppedAttributesCount (2) is
// absent because this SDK drops no resource attribute — it folds excess SERIES,
// which is a different thing and shows up as its own data point.
type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes,omitempty"`
}

// otlpScopeMetrics is ScopeMetrics: scope (1) + metrics (2), with the same
// schemaUrl omission as otlpResourceMetrics.
type otlpScopeMetrics struct {
	Scope   otlpScope    `json:"scope"`
	Metrics []otlpMetric `json:"metrics,omitempty"`
}

// otlpScope is InstrumentationScope: name (1) + version (2). A Meter normalises
// Name, so it is always present; Version is optional in the specification and
// omitted when the caller has none rather than emitted blank.
type otlpScope struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// otlpMetric is Metric: name (1) plus the data oneof — gauge (5), sum (7),
// histogram (9). Exactly one of the three pointers is non-nil, which is how a
// oneof is spelled in Go.
//
// description (2) and unit (3) are absent: a Meter records neither, and an
// empty string for each would be a placeholder.
type otlpMetric struct {
	Name      string         `json:"name"`
	Gauge     *otlpGauge     `json:"gauge,omitempty"`
	Sum       *otlpSum       `json:"sum,omitempty"`
	Histogram *otlpHistogram `json:"histogram,omitempty"`
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
	StartTimeUnixNano otlpUint64     `json:"startTimeUnixNano"`
	TimeUnixNano      otlpUint64     `json:"timeUnixNano"`
	AsDouble          *otlpDouble    `json:"asDouble,omitempty"`
	AsInt             *otlpInt64     `json:"asInt,omitempty"`
	Attributes        []otlpKeyValue `json:"attributes,omitempty"`
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
	StartTimeUnixNano otlpUint64     `json:"startTimeUnixNano"`
	TimeUnixNano      otlpUint64     `json:"timeUnixNano"`
	Count             otlpUint64     `json:"count"`
	Sum               *otlpDouble    `json:"sum"`
	BucketCounts      []otlpUint64   `json:"bucketCounts,omitempty"`
	ExplicitBounds    []otlpDouble   `json:"explicitBounds,omitempty"`
	Attributes        []otlpKeyValue `json:"attributes,omitempty"`
}

// otlpKeyValue is common.v1.KeyValue: key (1) + value (2). One attribute of a
// resource, a scope or a data point.
type otlpKeyValue struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

// otlpAnyValue is common.v1.AnyValue, restricted to the four SCALAR cases this
// SDK's attribute model has: stringValue (1), boolValue (2), intValue (3),
// doubleValue (4).
//
// arrayValue (5), kvlistValue (6) and bytesValue (7) are absent because
// AttrValue has no corresponding kind — homogeneous array attributes are
// deferred with a measured reason (ADR 0044 §Decision 2). Exactly one pointer
// is non-nil, and it is emitted even at its zero value, because a oneof member
// has explicit presence.
type otlpAnyValue struct {
	StringValue *string     `json:"stringValue,omitempty"`
	BoolValue   *bool       `json:"boolValue,omitempty"`
	IntValue    *otlpInt64  `json:"intValue,omitempty"`
	DoubleValue *otlpDouble `json:"doubleValue,omitempty"`
}
