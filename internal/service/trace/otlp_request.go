// Package trace — the OTLP payload tree: a Go mirror of
// opentelemetry/proto/{collector/trace,trace,common,resource}/v1, restricted to
// the fields this SDK produces.
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
// (rule 5): schemaUrl, droppedAttributesCount, droppedEventsCount,
// droppedLinksCount, and a scope's attributes.
package trace

// otlpRequest is ExportTraceServiceRequest — the body of a POST to /v1/traces.
// Exactly one resourceSpans entry, because one Tracer has exactly one Resource.
type otlpRequest struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

// otlpResourceSpans is ResourceSpans: resource (1) + scopeSpans (2). schemaUrl
// (3) is absent for the reason ADR 0044 §Decision 4 gives — it is optional,
// nothing here produces one, and an always-empty field is a placeholder.
type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

// otlpResource is Resource: attributes (1). droppedAttributesCount (2) is absent
// because this SDK drops no resource attribute — it drops whole SPANS when a
// Recorder overflows, which is a different thing and is counted separately.
type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes,omitempty"`
}

// otlpScopeSpans is ScopeSpans: scope (1) + spans (2), with the same schemaUrl
// omission as otlpResourceSpans.
type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans,omitempty"`
}

// otlpScope is InstrumentationScope: name (1) + version (2). A Tracer normalises
// Name, so it is always present; Version is optional in the specification and
// omitted when the caller has none rather than emitted blank.
type otlpScope struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// otlpSpan is Span, in field-number order: traceId (1), spanId (2), traceState
// (3), parentSpanId (4), name (5), kind (6), startTimeUnixNano (7),
// endTimeUnixNano (8), attributes (9), events (11), links (13), status (15),
// flags (16).
//
// Three encodings here are not the generic protobuf-JSON mapping:
//
//   - TraceID and SpanID are HEX, not base64. The OTLP specification overrides
//     the standard mapping by name: "the traceId and spanId byte arrays are
//     represented as case-insensitive hex-encoded strings; they are not
//     base64-encoded as is defined in the standard Protobuf JSON Mapping." A
//     base64 payload is accepted by nothing and looks plausible in a log.
//   - The two timestamps are fixed64, so they ride as DECIMAL STRINGS. A JSON
//     number is a double in most parsers and a nanosecond timestamp is past
//     2^53 permanently, so a number would lose its low bits — micro-scale jitter
//     that no dashboard can detect.
//   - Flags is fixed32, so it rides as a plain JSON NUMBER. The string rule is
//     for 64-bit integers only, and widening a 32-bit field to a string is as
//     wrong as narrowing a 64-bit one to a number.
//
// Status is a POINTER and omitted when unset, because STATUS_CODE_UNSET is the
// overwhelmingly common case and an all-default Status message carries exactly
// the information its absence does. Flags is emitted ALWAYS, for the reason the
// metrics mirror always emits isMonotonic: it is the field that says whether the
// span was sampled and whether its parent was remote, and a field that vanishes
// precisely when it carries the surprising answer is one a reader cannot trust.
// It is also never actually zero here — SpanFlagsHasIsRemote is always set.
type otlpSpan struct {
	TraceID           string         `json:"traceId"`
	SpanID            string         `json:"spanId"`
	TraceState        string         `json:"traceState,omitempty"`
	ParentSpanID      string         `json:"parentSpanId,omitempty"`
	Name              string         `json:"name"`
	Kind              int32          `json:"kind"`
	StartTimeUnixNano otlpUint64     `json:"startTimeUnixNano"`
	EndTimeUnixNano   otlpUint64     `json:"endTimeUnixNano"`
	Attributes        []otlpKeyValue `json:"attributes,omitempty"`
	Events            []otlpEvent    `json:"events,omitempty"`
	Links             []otlpLink     `json:"links,omitempty"`
	Status            *otlpStatus    `json:"status,omitempty"`
	Flags             uint32         `json:"flags"`
}

// otlpEvent is Span.Event: timeUnixNano (1), name (2), attributes (3).
// droppedAttributesCount (4) is absent — nothing here drops an event attribute.
type otlpEvent struct {
	TimeUnixNano otlpUint64     `json:"timeUnixNano"`
	Name         string         `json:"name"`
	Attributes   []otlpKeyValue `json:"attributes,omitempty"`
}

// otlpLink is Span.Link: traceId (1), spanId (2), traceState (3), attributes
// (4), flags (6). droppedAttributesCount (5) is absent.
//
// A link carries its own flags for the same reason a span does: the LINKED
// context's sampled bit tells a backend whether following the link will find
// anything, and its remoteness tells it whether the link crosses a process.
type otlpLink struct {
	TraceID    string         `json:"traceId"`
	SpanID     string         `json:"spanId"`
	TraceState string         `json:"traceState,omitempty"`
	Attributes []otlpKeyValue `json:"attributes,omitempty"`
	Flags      uint32         `json:"flags"`
}

// otlpStatus is Status: message (2), code (3). Field 1 is RESERVED in the schema
// — it held a removed `deprecated_code` — which is why this struct starts at 2
// and why a reader should not expect a field there.
//
// Message is omitted unless it is set, and StatusValue.Resolved guarantees it is
// set only alongside STATUS_CODE_ERROR: the schema says "message … SHOULD be
// used only if the code is ERROR".
type otlpStatus struct {
	Message string `json:"message,omitempty"`
	Code    int32  `json:"code"`
}

// otlpKeyValue is common.v1.KeyValue: key (1) + value (2).
type otlpKeyValue struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

// otlpAnyValue is common.v1.AnyValue, restricted to the four SCALAR cases this
// SDK's attribute model has: stringValue (1), boolValue (2), intValue (3),
// doubleValue (4).
//
// arrayValue (5), kvlistValue (6) and bytesValue (7) are absent because AttrValue
// has no corresponding kind (ADR 0044 §Decision 2). Exactly one pointer is
// non-nil, and it is emitted even at its zero value, because a oneof member has
// explicit presence: Bool("cache.hit", false) must encode as {"boolValue":false}
// and not as {}.
type otlpAnyValue struct {
	StringValue *string     `json:"stringValue,omitempty"`
	BoolValue   *bool       `json:"boolValue,omitempty"`
	IntValue    *otlpInt64  `json:"intValue,omitempty"`
	DoubleValue *otlpDouble `json:"doubleValue,omitempty"`
}
