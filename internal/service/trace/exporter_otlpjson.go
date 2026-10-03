// Package trace — OTLP/JSON encoder: SpansValue to the bytes an OTLP receiver
// accepts, implemented from the specification with encoding/json.
package trace

import (
	"io"
	"os"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/internal/otlp"
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

var (
	// otlpEncodeFailure is the wrap a rendering fault in the shared marshal leaves
	// under: this signal's EXPORT_FAILED, in its own words. Near-impossible — every
	// field of the tree is a Go primitive or one of the shared Marshalers — and
	// still typed rather than swallowed.
	otlpEncodeFailure = errs.WrapParams{
		Code:    coretrace.CodeExportFailed,
		Reason:  "EXPORT_FAILED",
		Public:  "The trace exporter failed to ship the spans",
		Private: "service/trace: the OTLP/JSON encoder could not render the payload",
	}

	// otlpWriteFailure is the wrap a writer fault leaves under when the
	// writer-bound exporter emits a document: this signal's EXPORT_FAILED.
	otlpWriteFailure = errs.WrapParams{
		Code:    coretrace.CodeExportFailed,
		Reason:  "EXPORT_FAILED",
		Public:  "The trace exporter failed to ship the spans",
		Private: "service/trace: OTLP/JSON exporter writer returned an error",
	}

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
	OTLPJSON = coretrace.RegisterExporter(newOTLPJSONExporter(otlpJSONExporterName, os.Stderr))
)

// otlpJSONExporter writes each batch to dst as one OTLP/JSON document.
//
// The shared stream serialises the single write: core/trace.SpanExporter
// requires concurrency safety and the writer is caller-supplied. Encoding
// happens before it, outside the lock, so a slow writer serialises callers
// without also serialising the work.
type otlpJSONExporter struct {
	// name is the exporter's registry key.
	name coretrace.ExporterName
	// stream is the newline-delimited document stream bound to the writer.
	stream *otlp.Stream
}

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
		Scope: otlp.ScopeOf(spans.Scope),
		Spans: rendered,
	}
	resource := otlpResourceSpans{
		Resource:   otlp.ResourceOf(spans.Resource),
		ScopeSpans: []otlpScopeSpans{scope},
	}
	//: marshal the tree with HTML escaping off — see otlp.Marshal.
	return otlp.Marshal(otlpRequest{ResourceSpans: []otlpResourceSpans{resource}}, &otlpEncodeFailure)
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
		StartTimeUnixNano: otlp.UnixNano(span.StartTime),
		EndTimeUnixNano:   otlp.UnixNano(span.EndTime),
		Attributes:        otlp.Attrs(span.Attrs),
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
			TimeUnixNano: otlp.UnixNano(event.Time),
			Name:         event.Name,
			Attributes:   otlp.Attrs(event.Attrs),
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
			Attributes: otlp.Attrs(link.Attrs),
			Flags:      otlpSpanFlags(link.Context, link.Context),
		})
	}
	//: hand back the rendered links.
	return out
}

// newOTLPJSONExporter is the shared constructor.
func newOTLPJSONExporter(name coretrace.ExporterName, dst io.Writer) *otlpJSONExporter {
	//: a writer-bound exporter whose only state is the shared stream's lock.
	return &otlpJSONExporter{name: name, stream: otlp.NewStream(dst, &otlpWriteFailure)}
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
	//: one terminated document, one write, under this signal's EXPORT_FAILED.
	return e.stream.Emit(doc)
}
