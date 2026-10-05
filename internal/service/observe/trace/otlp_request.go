package trace

import "github.com/kitsunium/sdk/internal/service/observe/internal/otlp"

// otlpRequest is ExportTraceServiceRequest — the body of a POST to /v1/traces.
// Exactly one resourceSpans entry, because one Tracer has exactly one Resource.
type otlpRequest struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

// otlpResourceSpans is ResourceSpans: resource (1) + scopeSpans (2). schemaUrl
// (3) is absent for the reason ADR 0044 §Decision 4 gives — it is optional,
// nothing here produces one, and an always-empty field is a placeholder.
type otlpResourceSpans struct {
	Resource   otlp.ResourceMessage `json:"resource"`
	ScopeSpans []otlpScopeSpans     `json:"scopeSpans"`
}

// otlpScopeSpans is ScopeSpans: scope (1) + spans (2), with the same schemaUrl
// omission as otlpResourceSpans.
type otlpScopeSpans struct {
	Scope otlp.ScopeMessage `json:"scope"`
	Spans []otlpSpan        `json:"spans,omitempty"`
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
	TraceID           string          `json:"traceId"`
	SpanID            string          `json:"spanId"`
	TraceState        string          `json:"traceState,omitempty"`
	ParentSpanID      string          `json:"parentSpanId,omitempty"`
	Name              string          `json:"name"`
	Kind              int32           `json:"kind"`
	StartTimeUnixNano otlp.Uint64     `json:"startTimeUnixNano"`
	EndTimeUnixNano   otlp.Uint64     `json:"endTimeUnixNano"`
	Attributes        []otlp.KeyValue `json:"attributes,omitempty"`
	Events            []otlpEvent     `json:"events,omitempty"`
	Links             []otlpLink      `json:"links,omitempty"`
	Status            *otlpStatus     `json:"status,omitempty"`
	Flags             uint32          `json:"flags"`
}

// otlpEvent is Span.Event: timeUnixNano (1), name (2), attributes (3).
// droppedAttributesCount (4) is absent — nothing here drops an event attribute.
type otlpEvent struct {
	TimeUnixNano otlp.Uint64     `json:"timeUnixNano"`
	Name         string          `json:"name"`
	Attributes   []otlp.KeyValue `json:"attributes,omitempty"`
}

// otlpLink is Span.Link: traceId (1), spanId (2), traceState (3), attributes
// (4), flags (6). droppedAttributesCount (5) is absent.
//
// A link carries its own flags for the same reason a span does: the LINKED
// context's sampled bit tells a backend whether following the link will find
// anything, and its remoteness tells it whether the link crosses a process.
type otlpLink struct {
	TraceID    string          `json:"traceId"`
	SpanID     string          `json:"spanId"`
	TraceState string          `json:"traceState,omitempty"`
	Attributes []otlp.KeyValue `json:"attributes,omitempty"`
	Flags      uint32          `json:"flags"`
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
