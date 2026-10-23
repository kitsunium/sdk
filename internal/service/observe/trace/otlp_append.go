package trace

import (
	"strconv"

	"github.com/kitsunium/sdk/internal/service/observe/internal/otlp"
)

// The capacity an OTLP/JSON document starts with, so the appends below grow it
// a few times rather than from nothing: the envelope, plus an estimate per
// span. An estimate, never a bound — append grows the buffer past it.
const (
	otlpDocEnvelopeBytes int = 256
	otlpDocSpanBytes     int = 640
	otlpDocDecimalBase   int = 10
)

// appendJSON appends the request to dst, byte for byte what otlp.Marshal
// renders for it — member order, omissions, a nil slice that is not omitted
// spelled null — without reflection and without a slice per scalar.
func (r *otlpRequest) appendJSON(dst []byte) []byte {
	//: resourceSpans, never omitted.
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "resourceSpans")
	//: a nil list is null, as encoding/json spells it.
	if r.ResourceSpans == nil {
		//: the literal.
		dst = otlp.AppendNull(dst)
		return append(dst, '}')
	}
	//: one entry per Resource.
	dst = append(dst, '[')
	for i := range r.ResourceSpans {
		//: comma-separated.
		if i > 0 {
			//: the separator.
			dst = append(dst, ',')
		}
		dst = r.ResourceSpans[i].appendJSON(dst)
	}
	return append(dst, ']', '}')
}

// appendJSON appends one ResourceSpans.
func (r *otlpResourceSpans) appendJSON(dst []byte) []byte {
	//: resource (1), then scopeSpans (2), never omitted.
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "resource")
	dst = r.Resource.AppendJSON(dst)
	dst = otlp.AppendKey(dst, "scopeSpans")
	//: a nil list is null.
	if r.ScopeSpans == nil {
		//: the literal.
		dst = otlp.AppendNull(dst)
		return append(dst, '}')
	}
	//: one entry per Scope.
	dst = append(dst, '[')
	for i := range r.ScopeSpans {
		//: comma-separated.
		if i > 0 {
			//: the separator.
			dst = append(dst, ',')
		}
		dst = r.ScopeSpans[i].appendJSON(dst)
	}
	return append(dst, ']', '}')
}

// appendJSON appends one ScopeSpans, its spans omitted when empty.
func (s *otlpScopeSpans) appendJSON(dst []byte) []byte {
	//: scope (1).
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "scope")
	dst = s.Scope.AppendJSON(dst)
	//: spans (2), omitted when empty.
	if len(s.Spans) > 0 {
		//: the batch, in recording order.
		dst = otlp.AppendKey(dst, "spans")
		dst = append(dst, '[')
		for i := range s.Spans {
			//: comma-separated.
			if i > 0 {
				//: the separator.
				dst = append(dst, ',')
			}
			dst = s.Spans[i].appendJSON(dst)
		}
		dst = append(dst, ']')
	}
	return append(dst, '}')
}

// appendJSON appends one Span, in field-number order.
func (s *otlpSpan) appendJSON(dst []byte) []byte {
	//: traceId (1) and spanId (2), hex, never omitted.
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "traceId")
	dst = otlp.AppendString(dst, s.TraceID)
	dst = otlp.AppendKey(dst, "spanId")
	dst = otlp.AppendString(dst, s.SpanID)
	//: traceState (3), omitted when empty.
	if s.TraceState != "" {
		//: the vendor list.
		dst = otlp.AppendKey(dst, "traceState")
		dst = otlp.AppendString(dst, s.TraceState)
	}
	//: parentSpanId (4), omitted for a root.
	if s.ParentSpanID != "" {
		//: hex.
		dst = otlp.AppendKey(dst, "parentSpanId")
		dst = otlp.AppendString(dst, s.ParentSpanID)
	}
	//: name (5), kind (6), and the two timestamps (7, 8).
	dst = otlp.AppendKey(dst, "name")
	dst = otlp.AppendString(dst, s.Name)
	dst = otlp.AppendKey(dst, "kind")
	dst = strconv.AppendInt(dst, int64(s.Kind), otlpDocDecimalBase)
	dst = otlp.AppendKey(dst, "startTimeUnixNano")
	dst = s.StartTimeUnixNano.AppendJSON(dst)
	dst = otlp.AppendKey(dst, "endTimeUnixNano")
	dst = s.EndTimeUnixNano.AppendJSON(dst)
	//: attributes (9), omitted when empty.
	if len(s.Attributes) > 0 {
		//: the repeated KeyValue.
		dst = otlp.AppendKey(dst, "attributes")
		dst = otlp.AppendKeyValues(dst, s.Attributes)
	}
	//: events (11), omitted when empty.
	if len(s.Events) > 0 {
		//: in recording order.
		dst = otlp.AppendKey(dst, "events")
		dst = appendEvents(dst, s.Events)
	}
	//: links (13), omitted when empty.
	if len(s.Links) > 0 {
		//: in declaration order.
		dst = otlp.AppendKey(dst, "links")
		dst = appendLinks(dst, s.Links)
	}
	//: status (15), omitted when unset.
	if s.Status != nil {
		//: message (2) when set, then code (3).
		dst = otlp.AppendKey(dst, "status")
		dst = s.Status.appendJSON(dst)
	}
	//: flags (16), always emitted.
	dst = otlp.AppendKey(dst, "flags")
	dst = strconv.AppendUint(dst, uint64(s.Flags), otlpDocDecimalBase)
	return append(dst, '}')
}

// appendEvents appends a span's events as a JSON array.
func appendEvents(dst []byte, events []otlpEvent) []byte {
	//: the opening bracket.
	dst = append(dst, '[')
	for i := range events {
		//: comma-separated.
		if i > 0 {
			//: the separator.
			dst = append(dst, ',')
		}
		//: timeUnixNano (1), name (2), attributes (3) when any.
		event := &events[i]
		dst = append(dst, '{')
		dst = otlp.AppendKey(dst, "timeUnixNano")
		dst = event.TimeUnixNano.AppendJSON(dst)
		dst = otlp.AppendKey(dst, "name")
		dst = otlp.AppendString(dst, event.Name)
		//: omitted when empty.
		if len(event.Attributes) > 0 {
			//: the repeated KeyValue.
			dst = otlp.AppendKey(dst, "attributes")
			dst = otlp.AppendKeyValues(dst, event.Attributes)
		}
		dst = append(dst, '}')
	}
	return append(dst, ']')
}

// appendLinks appends a span's links as a JSON array.
func appendLinks(dst []byte, links []otlpLink) []byte {
	//: the opening bracket.
	dst = append(dst, '[')
	for i := range links {
		//: comma-separated.
		if i > 0 {
			//: the separator.
			dst = append(dst, ',')
		}
		//: traceId (1), spanId (2), traceState (3) and attributes (4) when
		//: set, flags (6).
		link := &links[i]
		dst = append(dst, '{')
		dst = otlp.AppendKey(dst, "traceId")
		dst = otlp.AppendString(dst, link.TraceID)
		dst = otlp.AppendKey(dst, "spanId")
		dst = otlp.AppendString(dst, link.SpanID)
		//: omitted when empty.
		if link.TraceState != "" {
			//: the linked context's vendor list.
			dst = otlp.AppendKey(dst, "traceState")
			dst = otlp.AppendString(dst, link.TraceState)
		}
		//: omitted when empty.
		if len(link.Attributes) > 0 {
			//: the repeated KeyValue.
			dst = otlp.AppendKey(dst, "attributes")
			dst = otlp.AppendKeyValues(dst, link.Attributes)
		}
		dst = otlp.AppendKey(dst, "flags")
		dst = strconv.AppendUint(dst, uint64(link.Flags), otlpDocDecimalBase)
		dst = append(dst, '}')
	}
	return append(dst, ']')
}

// appendJSON appends one Status: its message when set, then its code.
func (s *otlpStatus) appendJSON(dst []byte) []byte {
	//: message (2), omitted when empty.
	dst = append(dst, '{')
	if s.Message != "" {
		//: escaped like any string.
		dst = otlp.AppendKey(dst, "message")
		dst = otlp.AppendString(dst, s.Message)
	}
	//: code (3), always emitted.
	dst = otlp.AppendKey(dst, "code")
	dst = strconv.AppendInt(dst, int64(s.Code), otlpDocDecimalBase)
	return append(dst, '}')
}

// otlpDocCapacity estimates the size of the document spans render into, so
// the buffer starts near it.
func otlpDocCapacity(spans []otlpSpan) int {
	//: the envelope, then one estimate per span.
	return otlpDocEnvelopeBytes + len(spans)*otlpDocSpanBytes
}
