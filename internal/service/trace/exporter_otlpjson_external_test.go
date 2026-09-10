package trace_test

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	svctrace "github.com/kitsunium/sdk/internal/service/trace"
)

// The fixture instants, chosen so every timestamp is past 2^53 and therefore
// proves the decimal-string rule rather than merely surviving it. 2^53 is
// 9007199254740992; each of these is five orders of magnitude larger.
const (
	fixtureStartNanos = int64(1_700_000_000_000_000_123)
	fixtureEndNanos   = int64(1_700_000_000_500_000_456)
	fixtureEventNanos = int64(1_700_000_000_250_000_000)
	fixtureRootStart  = int64(1_700_000_000_000_000_000)
	fixtureRootEnd    = int64(1_700_000_000_000_001_000)
)

// otlpGolden is the expected ExportTraceServiceRequest, assembled BY HAND from
// opentelemetry/proto/{collector/trace,trace,common,resource}/v1 and the OTLP
// specification's §JSON Protobuf Encoding — never by round-tripping this
// package's own output.
//
// That is the whole point of the file. A test that decoded the encoder's output
// and compared it to the input would prove self-consistency, which is exactly the
// property a wrong field name, a base64 identifier or a mis-numbered enum
// PRESERVES. The field numbers are named in the comments beside each fragment so
// the document can be checked against the .proto line by line.
//
// The fragments are joined with no separator; each carries its own punctuation.
var otlpGolden = strings.Join([]string{
	// ExportTraceServiceRequest.resource_spans = 1
	`{"resourceSpans":[{`,
	// ResourceSpans.resource = 1; Resource.attributes = 1
	// KeyValue.key = 1, KeyValue.value = 2; AnyValue.string_value = 1
	`"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"checkout"}}]},`,
	// ResourceSpans.scope_spans = 2; ScopeSpans.scope = 1
	// InstrumentationScope.name = 1, .version = 2
	`"scopeSpans":[{"scope":{"name":"github.com/kitsunium/sdk/pkg/v1/trace","version":"9.9.9"},`,
	// ScopeSpans.spans = 2
	`"spans":[`,

	// ── span 1: a SERVER span with a remote parent ───────────────────────────
	// Span.trace_id = 1 — HEX, not base64. The OTLP specification overrides the
	// standard protobuf-JSON mapping by name for exactly these two fields.
	`{"traceId":"4bf92f3577b34da6a3ce929d0e0e4736",`,
	// Span.span_id = 2
	`"spanId":"00f067aa0ba902b7",`,
	// Span.trace_state = 3 — the W3C header value, verbatim
	`"traceState":"congo=t61rcWkgMzE",`,
	// Span.parent_span_id = 4
	`"parentSpanId":"0102030405060708",`,
	// Span.name = 5
	`"name":"GET",`,
	// Span.kind = 6 — SPAN_KIND_SERVER = 2, as an INTEGER. OTLP forbids the
	// enum NAME form the generic proto3 JSON mapping allows.
	`"kind":2,`,
	// Span.start_time_unix_nano = 7 — fixed64, so a DECIMAL STRING
	`"startTimeUnixNano":"1700000000000000123",`,
	// Span.end_time_unix_nano = 8
	`"endTimeUnixNano":"1700000000500000456",`,
	// Span.attributes = 9 — sorted by key, one AnyValue oneof case each.
	// boolValue is emitted AT FALSE: a oneof member has explicit presence, so
	// omitting it would mean "no case selected", not "false".
	`"attributes":[`,
	`{"key":"cache.hit","value":{"boolValue":false}},`,
	`{"key":"http.request.method","value":{"stringValue":"GET"}},`,
	// AnyValue.int_value = 3 is an int64, so it is a decimal string too
	`{"key":"http.response.status_code","value":{"intValue":"503"}},`,
	// AnyValue.double_value = 4 is a double, so it stays a JSON number
	`{"key":"sample.ratio","value":{"doubleValue":0.25}}`,
	`],`,
	// Span.events = 11 (10 is dropped_attributes_count, which this SDK omits)
	// Event.time_unix_nano = 1, .name = 2, .attributes = 3
	`"events":[{"timeUnixNano":"1700000000250000000","name":"exception",`,
	`"attributes":[{"key":"exception.message","value":{"stringValue":"upstream refused"}}]}],`,
	// Span.links = 13 (12 is dropped_events_count, omitted)
	// Link.trace_id = 1, .span_id = 2, .trace_state = 3, .attributes = 4,
	// .flags = 6 (5 is dropped_attributes_count, omitted).
	// flags = 0x01 sampled | 0x100 HAS_IS_REMOTE = 257.
	`"links":[{"traceId":"0af7651916cd43dd8448eb211c80319c","spanId":"b7ad6b7169203331",`,
	`"attributes":[{"key":"link.kind","value":{"stringValue":"batch"}}],"flags":257}],`,
	// Span.status = 15 (14 is dropped_links_count, omitted)
	// Status.message = 2, .code = 3 — field 1 is RESERVED in the schema.
	// STATUS_CODE_ERROR = 2.
	`"status":{"message":"upstream refused","code":2},`,
	// Span.flags = 16 — fixed32, so a JSON NUMBER and not a string. Last,
	// because 16 follows 15, even though every .proto listing prints it beside
	// parent_span_id. 0x01 sampled | 0x100 HAS_IS_REMOTE | 0x200 IS_REMOTE = 769.
	`"flags":769},`,

	// ── span 2: a root INTERNAL span, exercising every omission ──────────────
	`{"traceId":"0af7651916cd43dd8448eb211c80319c",`,
	`"spanId":"b7ad6b7169203331",`,
	// traceState (3) absent: the vendor list is empty.
	// parentSpanId (4) absent: a root span's parent is the schema's own
	// "no parent", which is an ABSENT field and not sixteen zero digits.
	`"name":"charge",`,
	// SPAN_KIND_INTERNAL = 1
	`"kind":1,`,
	`"startTimeUnixNano":"1700000000000000000",`,
	`"endTimeUnixNano":"1700000000000001000",`,
	// attributes (9), events (11), links (13) absent: all empty.
	// status (15) absent: STATUS_CODE_UNSET carries what its absence carries.
	// flags = 0x01 | 0x100 = 257; no IS_REMOTE because there is no parent.
	`"flags":257}`,

	`]}]}]}`,
}, "")

// fixtureSpans builds the batch otlpGolden describes.
func fixtureSpans(t *testing.T) coretrace.SpansValue {
	t.Helper()
	parent := coretrace.SpanContextValue{
		TraceID: mustTraceID(t, "4bf92f3577b34da6a3ce929d0e0e4736"),
		SpanID:  mustSpanID(t, "0102030405060708"),
		Flags:   coretrace.FlagSampled,
		Remote:  true,
	}
	state, err := coretrace.ParseTraceState("congo=t61rcWkgMzE")
	if err != nil {
		t.Fatalf("ParseTraceState: %v", err)
	}
	linked := coretrace.SpanContextValue{
		TraceID: mustTraceID(t, "0af7651916cd43dd8448eb211c80319c"),
		SpanID:  mustSpanID(t, "b7ad6b7169203331"),
		Flags:   coretrace.FlagSampled,
	}
	server := coretrace.SpanValue{
		Context: coretrace.SpanContextValue{
			TraceID: parent.TraceID,
			SpanID:  mustSpanID(t, "00f067aa0ba902b7"),
			Flags:   coretrace.FlagSampled,
			State:   state,
		},
		Parent:    parent,
		Name:      "GET",
		Kind:      coretrace.SpanKindServer,
		StartTime: time.Unix(0, fixtureStartNanos).UTC(),
		EndTime:   time.Unix(0, fixtureEndNanos).UTC(),
		Attrs: coremetrics.SortAttrs([]coremetrics.AttrValue{
			coremetrics.String("http.request.method", "GET"),
			coremetrics.Int64("http.response.status_code", 503),
			coremetrics.Bool("cache.hit", false),
			coremetrics.Float64("sample.ratio", 0.25),
		}),
		Events: []coretrace.EventValue{{
			Time:  time.Unix(0, fixtureEventNanos).UTC(),
			Name:  coretrace.ExceptionEventName,
			Attrs: coremetrics.SortAttrs([]coremetrics.AttrValue{coremetrics.String(coretrace.ExceptionMessageKey, "upstream refused")}),
		}},
		Links: []coretrace.LinkValue{{
			Context: linked,
			Attrs:   coremetrics.SortAttrs([]coremetrics.AttrValue{coremetrics.String("link.kind", "batch")}),
		}},
		Status: coretrace.StatusValue{Code: coretrace.StatusError, Message: "upstream refused"},
	}
	root := coretrace.SpanValue{
		Context:   linked,
		Name:      "charge",
		Kind:      coretrace.SpanKindInternal,
		StartTime: time.Unix(0, fixtureRootStart).UTC(),
		EndTime:   time.Unix(0, fixtureRootEnd).UTC(),
	}
	return coretrace.SpansValue{
		Resource: coremetrics.ResourceValue{Attrs: coremetrics.SortAttrs([]coremetrics.AttrValue{
			coremetrics.String(coremetrics.ServiceNameKey, "checkout"),
		})},
		Scope: coremetrics.ScopeValue{Name: coretrace.DefaultScopeName, Version: "9.9.9"},
		Spans: []coretrace.SpanValue{server, root},
	}
}

// TestEncodeOTLPJSONMatchesTheHandWrittenSchemaDocument is the conformance gate.
func TestEncodeOTLPJSONMatchesTheHandWrittenSchemaDocument(t *testing.T) {
	doc, err := svctrace.EncodeOTLPJSON(fixtureSpans(t))
	if err != nil {
		t.Fatalf("EncodeOTLPJSON: %v", err)
	}
	if string(doc) != otlpGolden {
		t.Errorf("payload does not match the schema document\n got: %s\nwant: %s", doc, otlpGolden)
	}
}

// TestEncodeOTLPJSONIdentifiersAreHexNotBase64 pins the ONE place OTLP overrides
// the standard protobuf-JSON mapping. Base64 is what a generated encoder would
// emit, it is 24 characters of plausible-looking text, and no collector accepts
// it — so the failure is silent unless something asserts the alphabet.
func TestEncodeOTLPJSONIdentifiersAreHexNotBase64(t *testing.T) {
	doc, err := svctrace.EncodeOTLPJSON(fixtureSpans(t))
	if err != nil {
		t.Fatalf("EncodeOTLPJSON: %v", err)
	}
	if !bytes.Contains(doc, []byte(`"traceId":"4bf92f3577b34da6a3ce929d0e0e4736"`)) {
		t.Error("traceId is not the 32 lowercase hex digits the specification requires")
	}
	// The base64 of the same 16 bytes. Its presence would mean the encoder took
	// the generic []byte path.
	if bytes.Contains(doc, []byte("S/kvNXezTaajzpKdDg5HNg")) {
		t.Error("traceId was base64-encoded; OTLP/JSON requires hex for traceId and spanId")
	}
}

// TestEncodeOTLPJSONRefusesAnInvalidSpanContext pins the refusal: an all-zero
// identifier is what W3C Trace Context declares invalid, and the schema requires
// 16 and 8 real bytes.
func TestEncodeOTLPJSONRefusesAnInvalidSpanContext(t *testing.T) {
	batch := coretrace.SpansValue{Spans: []coretrace.SpanValue{{
		Name:      "orphan",
		StartTime: time.Unix(0, fixtureRootStart),
		EndTime:   time.Unix(0, fixtureRootEnd),
	}}}
	doc, err := svctrace.EncodeOTLPJSON(batch)
	if !errors.Is(err, svctrace.OTLPInvalidSpanContext) {
		t.Fatalf("want OTLPInvalidSpanContext, got %v", err)
	}
	if doc != nil {
		t.Error("a refused batch must produce no bytes at all")
	}
}

// TestEncodeOTLPJSONRefusesAnUnendedSpan pins the second refusal. A zero EndTime
// would encode as 0, which reads as "this span ended at the Unix epoch" and
// renders as a span 56 years long.
func TestEncodeOTLPJSONRefusesAnUnendedSpan(t *testing.T) {
	batch := fixtureSpans(t)
	batch.Spans[1].EndTime = time.Time{}
	doc, err := svctrace.EncodeOTLPJSON(batch)
	if !errors.Is(err, svctrace.OTLPSpanNotEnded) {
		t.Fatalf("want OTLPSpanNotEnded, got %v", err)
	}
	if doc != nil {
		t.Error("a refused batch must produce no bytes at all")
	}
}

// TestEncodeOTLPJSONKeepsAmpersandsUnescaped pins the HTML-escaping switch. The
// encoding/json default turns '&' into &, which makes every url.full
// attribute unreadable and makes this SDK's bytes differ from every other OTLP
// producer's for identical input.
func TestEncodeOTLPJSONKeepsAmpersandsUnescaped(t *testing.T) {
	batch := fixtureSpans(t)
	batch.Spans[1].Attrs = coremetrics.SortAttrs([]coremetrics.AttrValue{
		coremetrics.String("url.full", "https://api.example/search?q=a&limit=2"),
	})
	doc, err := svctrace.EncodeOTLPJSON(batch)
	if err != nil {
		t.Fatalf("EncodeOTLPJSON: %v", err)
	}
	if bytes.Contains(doc, []byte(`\u0026`)) {
		t.Error("'&' was HTML-escaped; SetEscapeHTML(false) is not in force")
	}
}

// TestEncodeOTLPJSONNamesNonFiniteDoubles pins the proto3-JSON spelling of a
// value encoding/json refuses outright. Without it one NaN-valued attribute would
// fail an entire export.
func TestEncodeOTLPJSONNamesNonFiniteDoubles(t *testing.T) {
	batch := fixtureSpans(t)
	batch.Spans[1].Attrs = coremetrics.SortAttrs([]coremetrics.AttrValue{
		coremetrics.Float64("a.nan", math.NaN()),
		coremetrics.Float64("b.inf", math.Inf(1)),
		coremetrics.Float64("c.neginf", math.Inf(-1)),
	})
	doc, err := svctrace.EncodeOTLPJSON(batch)
	if err != nil {
		t.Fatalf("EncodeOTLPJSON: %v", err)
	}
	for _, want := range []string{`{"doubleValue":"NaN"}`, `{"doubleValue":"Infinity"}`, `{"doubleValue":"-Infinity"}`} {
		if !bytes.Contains(doc, []byte(want)) {
			t.Errorf("missing %s in %s", want, doc)
		}
	}
}

// TestEncodeOTLPJSONNeverEmitsTheUnixNanoGarbageValue pins the timestamp guard.
// time.Time.UnixNano is documented as UNDEFINED out of range, and the zero Time
// is that case: it returns -6795364578871345152, which cast to uint64 reads as
// the year 2339 — a plausible wrong date no dashboard can detect.
func TestEncodeOTLPJSONNeverEmitsTheUnixNanoGarbageValue(t *testing.T) {
	batch := fixtureSpans(t)
	batch.Spans[1].Events = []coretrace.EventValue{{Name: "unstamped"}}
	doc, err := svctrace.EncodeOTLPJSON(batch)
	if err != nil {
		t.Fatalf("EncodeOTLPJSON: %v", err)
	}
	if bytes.Contains(doc, []byte("11651404478871345152")) || bytes.Contains(doc, []byte("-6795364578871345152")) {
		t.Errorf("the undefined UnixNano value reached the wire: %s", doc)
	}
	if !bytes.Contains(doc, []byte(`"timeUnixNano":"0"`)) {
		t.Errorf("an unset instant must encode as 0, the schema's unknown timestamp: %s", doc)
	}
}

// mustTraceID parses a hex trace id or fails the test.
func mustTraceID(t *testing.T, text string) coretrace.TraceID {
	t.Helper()
	id, err := coretrace.ParseTraceID(text)
	if err != nil {
		t.Fatalf("ParseTraceID(%q): %v", text, err)
	}
	return id
}

// mustSpanID parses a hex span id or fails the test.
func mustSpanID(t *testing.T, text string) coretrace.SpanID {
	t.Helper()
	id, err := coretrace.ParseSpanID(text)
	if err != nil {
		t.Fatalf("ParseSpanID(%q): %v", text, err)
	}
	return id
}
