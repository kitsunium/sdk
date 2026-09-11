// Package trace — OTLP/JSON encoder: SpansValue to the bytes an OTLP receiver
// accepts, implemented from the specification with encoding/json.
package trace

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"os"
	"strconv"
	"sync"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// otlpJSONExporterName is the registered name of the default OTLP/JSON span
// exporter, which writes to stderr (ADR 0030). It names the ENCODING, not the
// protocol, because an OTLP/protobuf encoder would register beside it.
const otlpJSONExporterName coretrace.ExporterName = "otlpjson"

// The three SpanFlags masks, copied from opentelemetry/proto/trace/v1.SpanFlags.
//
// The pair is a presence protocol rather than two independent bits: bit 8 says
// "bit 9 is meaningful". Without it a receiver cannot tell "the parent is local"
// from "this producer does not track remoteness", and the two lead to different
// service maps.
const (
	// spanFlagsTraceFlagsMask is SPAN_FLAGS_TRACE_FLAGS_MASK: the low byte
	// carries the W3C trace-flags of this span's own context.
	spanFlagsTraceFlagsMask uint32 = 0x0000_00FF
	// spanFlagsHasIsRemote is SPAN_FLAGS_CONTEXT_HAS_IS_REMOTE_MASK: this
	// producer knows whether the context is remote, so the next bit is real.
	spanFlagsHasIsRemote uint32 = 0x0000_0100
	// spanFlagsIsRemote is SPAN_FLAGS_CONTEXT_IS_REMOTE_MASK: the context came
	// from another process.
	spanFlagsIsRemote uint32 = 0x0000_0200
)

// The three spellings proto3 JSON gives a non-finite double. A double is "a
// number or one of the special string values 'NaN', 'Infinity', and
// '-Infinity'", so a NaN-valued attribute is expressible rather than fatal —
// unlike encoding/json's own float path, which refuses it outright.
const (
	otlpNaN         string = `"NaN"`
	otlpPosInfinity string = `"Infinity"`
	otlpNegInfinity string = `"-Infinity"`
)

// otlpDocumentTerminator ends each document a WRITER-bound OTLP exporter emits,
// so a stream of exports is newline-delimited JSON. It is deliberately NOT part
// of what EncodeOTLPJSON returns: that is the HTTP body, and a body is one
// document.
const otlpDocumentTerminator byte = '\n'

// otlpIntBufferSize pre-sizes a quoted 64-bit decimal: 20 digits, a sign, two
// quotes, rounded up.
const otlpIntBufferSize int = 24

// OTLPJSON is the default OTLP/JSON span exporter, registered to write each
// batch to stderr as one newline-terminated document. Use NewOTLPJSONExporter
// for a custom writer/name, EncodeOTLPJSON for the bytes alone, or
// NewOTLPHTTPExporter to actually ship them to a collector.
//
// stderr, not stdout, for the reason ADR 0030 gives in full: importing a package
// must never arm a writer on a stream the process may be using as a protocol
// channel. This instance is a diagnostic — the production path is
// NewOTLPHTTPExporter, which is never registered because arming a network client
// on import would be strictly worse than arming a writer.
var OTLPJSON = coretrace.RegisterExporter(newOTLPJSONExporter(otlpJSONExporterName, os.Stderr))

// otlpJSONExporter writes each batch to dst as one OTLP/JSON document.
//
// mu serialises the single dst.Write: core/trace.SpanExporter requires
// concurrency safety and dst is caller-supplied. Encoding happens outside the
// lock, so a slow writer serialises callers without also serialising the work.
type otlpJSONExporter struct {
	mu   sync.Mutex
	name coretrace.ExporterName
	dst  io.Writer
}

// otlpInt64 is a signed 64-bit integer rendered as a DECIMAL STRING — the
// proto3 JSON mapping OTLP inherits. The reason is range, not taste: a JSON
// number is a double in most parsers, so an int64 past 2^53 loses its low bits.
type otlpInt64 int64

// otlpUint64 is an unsigned 64-bit integer rendered as a decimal string, for the
// same reason otlpInt64 is: fixed64 and uint64 both map to a string. Every
// timestamp in this payload uses it, and every one of them is past 2^53.
type otlpUint64 uint64

// otlpDouble is an IEEE-754 double rendered as a JSON number, or as one of the
// three quoted spellings proto3 JSON gives a non-finite value.
type otlpDouble float64

// EncodeOTLPJSON renders spans as ONE OTLP/JSON ExportTraceServiceRequest —
// exactly the bytes that go in the body of a POST to /v1/traces under
// Content-Type: application/json.
//
// It is the encoder half of this package's OTLP support and it does no I/O at
// all, so it is usable and testable on its own: hand it a batch, compare the
// bytes to the schema. The emitter half (NewOTLPHTTPExporter) calls exactly this
// function and adds only the transport. That split is not tidiness — an encoding
// defect and a network defect have different reproductions, different evidence
// and different fixes, and a single Export that did both would make every OTLP
// question start with "is the collector up?".
//
// The batch maps onto the payload without a regrouping pass:
//
//	SpansValue         -> resourceSpans[0]
//	  .Resource        ->   .resource.attributes
//	  .Scope           ->   .scopeSpans[0].scope
//	  .Spans[i]        ->   .scopeSpans[0].spans[]
//
// It REFUSES rather than emits a payload the schema cannot express: a span
// carrying an all-zero trace-id or span-id (OTLPInvalidSpanContext), and a span
// with no end time (OTLPSpanNotEnded). Both are STRUCTURE — a Tracer mints both
// identifiers and End stamps the clock — so a batch can only carry either if it
// was hand-built or the span never ended, and each fails on the first export or
// never.
func EncodeOTLPJSON(spans coretrace.SpansValue) (doc []byte, err error) {
	//: render every span first; every refusal happens here, before a byte is
	//: produced. A half-payload would be a batch missing spans, and a receiver
	//: cannot tell that from a request that made fewer of them.
	rendered, buildErr := otlpSpans(spans.Spans)
	//: surface the typed refusal.
	if buildErr != nil {
		//: nothing is emitted.
		return nil, buildErr
	}
	//: the two collapsed levels are literal single-element slices: one Tracer
	//: is one Resource and one Scope, so there is nothing to group.
	scope := otlpScopeSpans{
		Scope: otlpScope{Name: spans.Scope.Name, Version: spans.Scope.Version},
		Spans: rendered,
	}
	resource := otlpResourceSpans{
		Resource:   otlpResource{Attributes: otlpAttrs(spans.Resource.Attrs)},
		ScopeSpans: []otlpScopeSpans{scope},
	}
	//: marshal the tree with HTML escaping off — see marshalOTLPJSON.
	return marshalOTLPJSON(otlpRequest{ResourceSpans: []otlpResourceSpans{resource}})
}

// marshalOTLPJSON renders request with HTML escaping DISABLED and no trailing
// newline.
//
// encoding/json escapes '<', '>' and '&' into their \u00xx forms by default, a
// defence for JSON embedded in a <script> element. An OTLP body never is, and
// OTel-conventional span attributes carry URLs (url.full, http.route) whose
// query separator is exactly '&' — so the default turns a readable payload into
// an unreadable one for no gain, and makes this SDK's bytes differ from every
// other OTLP producer's for identical input. json.Encoder is the only way to
// turn it off, and it appends a newline a single-document body must not carry.
func marshalOTLPJSON(request otlpRequest) (doc []byte, err error) {
	//: encode into a local buffer so the escaping switch is available.
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	//: '&' in a URL attribute stays '&'.
	encoder.SetEscapeHTML(false)
	//: the only failure mode left is a type encoding/json cannot render, and
	//: every field of this tree is a Go primitive or a Marshaler defined here.
	if encodeErr := encoder.Encode(request); encodeErr != nil {
		//: report it typed rather than swallowing an impossible case.
		return nil, errs.Wrap(encodeErr, errs.WrapParams{
			Code:    coretrace.CodeExportFailed,
			Reason:  "EXPORT_FAILED",
			Public:  "The trace exporter failed to ship the spans",
			Private: "service/trace: the OTLP/JSON encoder could not render the payload",
		})
	}
	//: Encode appends a newline; the HTTP body is one document, so drop it.
	return bytes.TrimSuffix(buf.Bytes(), []byte{otlpDocumentTerminator}), nil
}

// otlpSpans renders a batch, validating each span before any of them is encoded.
func otlpSpans(spans []coretrace.SpanValue) (rendered []otlpSpan, err error) {
	//: an empty batch renders as an omitted spans array, not as an error.
	if len(spans) == 0 {
		//: omitted by the omitempty tag.
		return nil, nil
	}
	//: exactly-sized: one wire span per recorded span.
	out := make([]otlpSpan, 0, len(spans))
	//: spans keep the order the recorder collected them in.
	for _, span := range spans {
		//: a span the schema cannot express aborts the whole document.
		if checkErr := checkOTLPSpan(span); checkErr != nil {
			//: surface the typed refusal.
			return nil, checkErr
		}
		//: render it in field-number order.
		out = append(out, otlpSpanOf(span))
	}
	//: hand back the rendered batch.
	return out, nil
}

// checkOTLPSpan refuses a span the schema cannot carry honestly.
func checkOTLPSpan(span coretrace.SpanValue) error {
	//: the two identifiers are required, and their all-zero forms are the ones
	//: W3C Trace Context declares invalid — a receiver handed either drops the
	//: span or attaches it to a trace nobody can join.
	if !span.Context.IsValid() {
		//: the span NAME is structure and safe to echo; the ids are not
		//: secret, but they are noise in a log line about a shape defect.
		return errs.Wrap(OTLPInvalidSpanContext, errs.WrapParams{}, errs.String("span", span.Name))
	}
	//: endTimeUnixNano is required. A zero would claim the span ended at the
	//: Unix epoch, which renders as a span 56 years long.
	if span.EndTime.IsZero() {
		//: name the span, which is structure and safe to echo.
		return errs.Wrap(OTLPSpanNotEnded, errs.WrapParams{}, errs.String("span", span.Name))
	}
	//: expressible.
	return nil
}

// otlpSpanOf renders one span in the schema's field-number order.
func otlpSpanOf(span coretrace.SpanValue) otlpSpan {
	//: a root span's parent is the invalid zero value, which the schema spells
	//: as an ABSENT parentSpanId rather than sixteen zeroes.
	parent := ""
	//: only a real parent is named.
	if span.Parent.IsValid() {
		//: hex, like every identifier on this wire.
		parent = span.Parent.SpanID.String()
	}
	//: field-number order: 1,2,3,4,5,6,7,8,9,11,13,15,16.
	return otlpSpan{
		TraceID:           span.Context.TraceID.String(),
		SpanID:            span.Context.SpanID.String(),
		TraceState:        span.Context.State.String(),
		ParentSpanID:      parent,
		Name:              span.Name,
		Kind:              int32(span.Kind.Resolved()),
		StartTimeUnixNano: otlpUnixNano(span.StartTime),
		EndTimeUnixNano:   otlpUnixNano(span.EndTime),
		Attributes:        otlpAttrs(span.Attrs),
		Events:            otlpEvents(span.Events),
		Links:             otlpLinks(span.Links),
		Status:            otlpStatusOf(span.Status),
		Flags:             otlpSpanFlags(span.Context, span.Parent),
	}
}

// otlpSpanFlags packs the W3C trace-flags byte and the parent's remoteness into
// the fixed32 `flags` field.
//
// The trace-flags half is SANITIZED: the same undefined-bits requirement that
// governs the traceparent header applies here, because this field carries
// "the W3C trace flags" — emitting a bit this version does not define would
// claim a meaning the schema has not assigned.
//
// SpanFlagsHasIsRemote is ALWAYS set, which is what makes the remoteness bit
// readable at all: without it a receiver cannot distinguish "the parent is
// local" from "this producer does not track it", and this producer does.
func otlpSpanFlags(context, parent coretrace.SpanContextValue) uint32 {
	//: the low byte is this span's own trace-flags, masked to the defined bits.
	flags := uint32(context.Flags.Sanitized()) & spanFlagsTraceFlagsMask
	//: this producer always knows, so bit 8 is always raised.
	flags |= spanFlagsHasIsRemote
	//: bit 9 says the parent ran in another process — a service boundary.
	if parent.IsValid() && parent.Remote {
		//: raise it.
		flags |= spanFlagsIsRemote
	}
	//: the packed field.
	return flags
}

// otlpStatusOf renders a span's status, or nil when there is nothing to say.
//
// An UNSET status is OMITTED rather than emitted as {"code":0}. STATUS_CODE_UNSET
// is the schema's default and the overwhelmingly common case, so emitting it
// would add a message to every span that carries exactly the information its
// absence does — and unlike the metrics encoder's three always-emitted fields,
// nothing here has explicit presence to preserve.
func otlpStatusOf(status coretrace.StatusValue) *otlpStatus {
	//: Resolved already dropped a message that had no error to belong to.
	resolved := status.Resolved()
	//: nothing recorded — omit the whole message.
	if resolved.IsUnset() {
		//: absent.
		return nil
	}
	//: message (2) then code (3), in field-number order.
	return &otlpStatus{Message: resolved.Message, Code: int32(resolved.Code)}
}

// otlpEvents renders a span's events. An empty set stays nil, which
// encoding/json omits — the proto3 rule for an empty repeated field.
func otlpEvents(events []coretrace.EventValue) []otlpEvent {
	//: the common span has no events at all.
	if len(events) == 0 {
		//: omitted by the omitempty tag.
		return nil
	}
	//: exactly-sized, in recording order.
	out := make([]otlpEvent, 0, len(events))
	//: field-number order: 1,2,3.
	for _, event := range events {
		//: one Event message per recorded point.
		out = append(out, otlpEvent{
			TimeUnixNano: otlpUnixNano(event.Time),
			Name:         event.Name,
			Attributes:   otlpAttrs(event.Attrs),
		})
	}
	//: hand back the rendered events.
	return out
}

// otlpLinks renders a span's links. Every link reaching here is valid —
// normalizeLinks dropped the ones that named no span.
func otlpLinks(links []coretrace.LinkValue) []otlpLink {
	//: the common span has no links at all.
	if len(links) == 0 {
		//: omitted by the omitempty tag.
		return nil
	}
	//: exactly-sized, in declaration order.
	out := make([]otlpLink, 0, len(links))
	//: field-number order: 1,2,3,4,6.
	for _, link := range links {
		//: a link's flags describe the LINKED context, not this span's.
		out = append(out, otlpLink{
			TraceID:    link.Context.TraceID.String(),
			SpanID:     link.Context.SpanID.String(),
			TraceState: link.Context.State.String(),
			Attributes: otlpAttrs(link.Attrs),
			Flags:      otlpSpanFlags(link.Context, link.Context),
		})
	}
	//: hand back the rendered links.
	return out
}

// otlpAttrs renders an attribute set as the repeated KeyValue every OTLP message
// spells its dimensions with. An empty set stays nil, which encoding/json omits.
func otlpAttrs(attrs []coremetrics.AttrValue) []otlpKeyValue {
	//: the dimensionless case has no attributes array at all.
	if len(attrs) == 0 {
		//: omitted by the omitempty tag.
		return nil
	}
	//: exactly-sized; the set is already sorted by Key.
	pairs := make([]otlpKeyValue, len(attrs))
	//: one KeyValue per dimension, in the batch's canonical order.
	for i, attr := range attrs {
		//: the AnyValue oneof carries the TYPE.
		pairs[i] = otlpKeyValueOf(attr)
	}
	//: hand back the rendered set.
	return pairs
}

// otlpKeyValueOf maps one typed attribute onto a KeyValue whose AnyValue names
// the attribute's kind.
//
// Exactly one AnyValue field is non-nil, and it is emitted even when it holds the
// type's zero: a oneof member has explicit presence, so an absent field means
// "no case selected", not "the default".
func otlpKeyValueOf(attr coremetrics.AttrValue) otlpKeyValue {
	//: one oneof case per attribute kind.
	switch attr.Kind() {
	//: the common dimension.
	case coremetrics.AttrKindString:
		//: stringValue.
		return otlpKeyValue{Key: attr.Key, Value: otlpAnyValue{StringValue: new(attr.Str())}}
	//: a flag.
	case coremetrics.AttrKindBool:
		//: boolValue — false is a value, not an absence.
		return otlpKeyValue{Key: attr.Key, Value: otlpAnyValue{BoolValue: new(attr.Bool())}}
	//: a signed 64-bit integer, which rides as a decimal string.
	case coremetrics.AttrKindInt64:
		//: intValue.
		return otlpKeyValue{Key: attr.Key, Value: otlpAnyValue{IntValue: new(otlpInt64(attr.Int64()))}}
	//: an IEEE-754 double, non-finite values included.
	case coremetrics.AttrKindFloat64:
		//: doubleValue.
		return otlpKeyValue{Key: attr.Key, Value: otlpAnyValue{DoubleValue: new(otlpDouble(attr.Float64()))}}
	//: AttrKindInvalid never reaches a batch — SortAttrs panics on it at the
	//: call site that wrote it.
	default:
		//: an empty AnyValue selects no case, which is what "no value" is.
		return otlpKeyValue{Key: attr.Key}
	}
}

// otlpUnixNano converts a wall-clock instant to the fixed64 nanosecond timestamp
// OTLP carries, mapping an unset instant onto 0.
//
// The guard is not defensive noise. time.Time's own documentation says UnixNano's
// "result is undefined if the Unix time in nanoseconds cannot be represented by
// an int64", and the zero Time is exactly that case: it returns a large negative
// number which, cast to uint64, becomes a timestamp several centuries in the
// future. An event stamped by a manual clock left at its zero is the reachable
// path, and it would ship silently wrong rather than visibly empty.
func otlpUnixNano(instant time.Time) otlpUint64 {
	//: the unset instant, and a pre-1970 one, have no unsigned spelling.
	if instant.IsZero() || instant.UnixNano() < 0 {
		//: the schema's own "unknown" value, rather than a wrapped one.
		return 0
	}
	//: in range and positive.
	return otlpUint64(instant.UnixNano())
}

// newOTLPJSONExporter is the shared constructor.
func newOTLPJSONExporter(name coretrace.ExporterName, dst io.Writer) *otlpJSONExporter {
	//: a stateless writer-bound exporter.
	return &otlpJSONExporter{name: name, dst: dst}
}

// NewOTLPJSONExporter returns a SpanExporter writing each batch to dst as one
// newline-terminated OTLP/JSON document. It is NOT added to the registry — bind
// it yourself or call Export directly.
func NewOTLPJSONExporter(name coretrace.ExporterName, dst io.Writer) coretrace.SpanExporter {
	//: hand back the concrete exporter behind the interface.
	return newOTLPJSONExporter(name, dst)
}

// Name implements core/trace.SpanExporter.
func (e *otlpJSONExporter) Name() coretrace.ExporterName {
	//: the registered name.
	return e.name
}

// Export encodes the whole batch, then writes it once, so a refusal leaves dst
// untouched and the only remaining error surface is the single write.
func (e *otlpJSONExporter) Export(spans coretrace.SpansValue) error {
	//: encode + validate first; nothing is written if either fails.
	doc, err := EncodeOTLPJSON(spans)
	//: an unusable span context or an unended span is reported typed.
	if err != nil {
		//: the sentinel already carries the code, reason and public message.
		return err
	}
	//: terminate the document so consecutive exports do not run together.
	doc = append(doc, otlpDocumentTerminator)
	//: single write — serialised so concurrent Exports cannot interleave
	//: partial documents into a non-atomic dst.
	e.mu.Lock()
	_, writeErr := e.dst.Write(doc)
	e.mu.Unlock()
	//: success fast-path.
	if writeErr == nil {
		//: batch written.
		return nil
	}
	//: wrap the writer fault with the dotted-quad code.
	return errs.Wrap(writeErr, errs.WrapParams{
		Code:    coretrace.CodeExportFailed,
		Reason:  "EXPORT_FAILED",
		Public:  "The trace exporter failed to ship the spans",
		Private: "service/trace: OTLP/JSON exporter writer returned an error",
	})
}

// MarshalJSON renders the integer as a quoted decimal string.
func (v otlpInt64) MarshalJSON() (encoded []byte, err error) {
	//: quote, digits, quote — no escaping is possible inside a decimal.
	out := make([]byte, 0, otlpIntBufferSize)
	out = append(out, '"')
	out = strconv.AppendInt(out, int64(v), decimalBase)
	//: json.Marshal never sees an error from a decimal rendering.
	return append(out, '"'), nil
}

// MarshalJSON renders the integer as a quoted decimal string.
func (v otlpUint64) MarshalJSON() (encoded []byte, err error) {
	//: quote, digits, quote.
	out := make([]byte, 0, otlpIntBufferSize)
	out = append(out, '"')
	out = strconv.AppendUint(out, uint64(v), decimalBase)
	//: json.Marshal never sees an error from a decimal rendering.
	return append(out, '"'), nil
}

// MarshalJSON renders the double shortest-round-trip, or names it when it is not
// finite. encoding/json refuses NaN and ±Inf outright, so without this type a
// single NaN-valued attribute would fail the whole export.
func (v otlpDouble) MarshalJSON() (encoded []byte, err error) {
	//: a value that is not a number has a name rather than a rendering.
	value := float64(v)
	//: NaN first — it fails every ordered comparison below.
	if math.IsNaN(value) {
		//: the schema's spelling, quoted.
		return []byte(otlpNaN), nil
	}
	//: +Inf.
	if math.IsInf(value, 1) {
		//: the schema's spelling, quoted.
		return []byte(otlpPosInfinity), nil
	}
	//: -Inf.
	if math.IsInf(value, -1) {
		//: the schema's spelling, quoted.
		return []byte(otlpNegInfinity), nil
	}
	//: finite: shortest representation that round-trips, which is a JSON
	//: number in every form strconv produces.
	return []byte(formatFloat(value)), nil
}
