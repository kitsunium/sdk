// Package metrics_test — the OTLP/JSON encoder, checked against the schema
// rather than against itself.
//
// Every expected document below was written BY HAND from
// opentelemetry/proto/metrics/v1/metrics.proto, the OTLP specification's
// "JSON Protobuf Encoding" section and the proto3 JSON mapping. None of it was
// produced by running the encoder: a test that decodes an encoder's own output
// proves the encoder is self-consistent, which is exactly the property a wrong
// field name or a mis-numbered enum preserves.
package metrics_test

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcmetrics "github.com/kitsunium/sdk/internal/service/metrics"
)

// otlpWindow is the startTimeUnixNano/timeUnixNano pair every data point
// carries. The snapshot holds ONE pair; the encoder copies it down, which is
// what ADR 0044 §Decision 9 means by "copied DOWN onto every data point".
const otlpWindow = `"startTimeUnixNano":"1767323045000000000",` +
	`"timeUnixNano":"1767323055000000000"`

// otlpGolden is the whole ExportMetricsServiceRequest for otlpFullSnapshot,
// assembled from the schema one message at a time.
//
// Field order inside each object is the schema's FIELD-NUMBER order, which is
// what makes each line below checkable against the .proto without reference to
// the Go code: NumberDataPoint puts attributes at 7, AFTER the value at 4/6,
// because it replaced a labels field that used to sit at 1.
const otlpGolden = `{"resourceMetrics":[{` +
	// Resource (resource.proto): attributes = 1. Carried ONCE per payload.
	`"resource":{"attributes":[` +
	`{"key":"deployment.environment","value":{"stringValue":"prod"}},` +
	`{"key":"service.name","value":{"stringValue":"orders"}}` +
	`]},` +
	// ScopeMetrics: scope = 1, metrics = 2. One entry — one Meter, one Scope.
	`"scopeMetrics":[{"scope":{"name":"github.com/acme/orders","version":"1.4.0"},"metrics":[` +
	// Metric: name = 1; the data oneof puts gauge at 5, sum at 7, histogram
	// at 9. Sum: dataPoints = 1, aggregationTemporality = 2, isMonotonic = 3.
	// AGGREGATION_TEMPORALITY_CUMULATIVE is the integer 2 — OTLP/JSON forbids
	// the enum NAME.
	`{"name":"http.server.requests","sum":{"dataPoints":[{` +
	otlpWindow + `,` +
	// NumberDataPoint.as_int is an sfixed64, so a decimal STRING.
	`"asInt":"4200",` +
	// KeyValue/AnyValue: int_value is an int64 and is a string too, which is
	// the whole point of the typed attribute model — 503 stays an integer.
	`"attributes":[` +
	`{"key":"http.request.method","value":{"stringValue":"GET"}},` +
	`{"key":"http.response.status_code","value":{"intValue":"503"}}` +
	`]}],"aggregationTemporality":2,"isMonotonic":true}},` +
	// An UpDownCounter: same Sum message, is_monotonic false. Both the false
	// bool and the zero as_int are EMITTED — a oneof member and a
	// distinguishing flag must not vanish when they hold their zero.
	`{"name":"queue.depth","sum":{"dataPoints":[{` +
	otlpWindow + `,"asInt":"0"}],` +
	`"aggregationTemporality":2,"isMonotonic":false}},` +
	// Gauge: data_points = 1 and nothing else — no aggregation_temporality
	// field exists on this message.
	`{"name":"process.cpu.utilization","gauge":{"dataPoints":[{` +
	otlpWindow + `,` +
	// NumberDataPoint.as_double is a double, so a JSON number, not a string.
	`"asDouble":0.25,` +
	`"attributes":[{"key":"cache.hit","value":{"boolValue":false}}]}]}},` +
	// Histogram: data_points = 1, aggregation_temporality = 2 (integer 1 for
	// DELTA). HistogramDataPoint: count = 4 (fixed64 -> string), sum = 5
	// (optional double -> number, always emitted), bucket_counts = 6
	// (repeated fixed64 -> strings), explicit_bounds = 7 (repeated double).
	`{"name":"http.server.duration","histogram":{"dataPoints":[{` +
	otlpWindow + `,` +
	`"count":"6","sum":1.5,"bucketCounts":["1","2","3"],"explicitBounds":[0.005,0.1]}],` +
	`"aggregationTemporality":1}}` +
	`]}]}]}`

// The collection window every golden document is written against, and its
// nanosecond spelling. OTLP carries a fixed64, so the JSON is a decimal string.
var (
	otlpStart = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	otlpEnd   = time.Date(2026, 1, 2, 3, 4, 15, 0, time.UTC)
)

// otlpFullSnapshot builds the snapshot otlpGolden describes: two sums (one of
// each monotonicity), a gauge and a delta histogram.
func otlpFullSnapshot() coremetrics.SnapshotValue {
	return coremetrics.SnapshotValue{
		Resource: coremetrics.ResourceValue{Attrs: []coremetrics.AttrValue{
			coremetrics.String("deployment.environment", "prod"),
			coremetrics.String(coremetrics.ServiceNameKey, "orders"),
		}},
		Scope:     coremetrics.ScopeValue{Name: "github.com/acme/orders", Version: "1.4.0"},
		StartTime: otlpStart,
		Time:      otlpEnd,
		Sums: map[string]coremetrics.SumMetricValue{
			"http.server.requests": {
				Temporality: coremetrics.TemporalityCumulative,
				Monotonic:   true,
				Points: []coremetrics.SumValue{{
					Attrs: []coremetrics.AttrValue{
						coremetrics.String("http.request.method", "GET"),
						coremetrics.Int64("http.response.status_code", 503),
					},
					Value: 4200,
				}},
			},
			"queue.depth": {
				Temporality: coremetrics.TemporalityCumulative,
				Monotonic:   false,
				Points:      []coremetrics.SumValue{{Value: 0}},
			},
		},
		Gauges: map[string]coremetrics.GaugeMetricValue{
			"process.cpu.utilization": {Points: []coremetrics.GaugeValue{{
				Attrs: []coremetrics.AttrValue{coremetrics.Bool("cache.hit", false)},
				Value: 0.25,
			}}},
		},
		Histograms: map[string]coremetrics.HistogramMetricValue{
			"http.server.duration": {
				Temporality: coremetrics.TemporalityDelta,
				Points: []coremetrics.HistogramValue{{
					Bounds: []float64{0.005, 0.1},
					Counts: []uint64{1, 2, 3},
					Sum:    1.5,
					Count:  6,
				}},
			},
		},
	}
}

// TestEncodeOTLPJSONMatchesTheSchema is the load-bearing assertion: the encoder
// produces the bytes the specification describes, to the byte.
func TestEncodeOTLPJSONMatchesTheSchema(t *testing.T) {
	t.Parallel()
	doc, err := svcmetrics.EncodeOTLPJSON(otlpFullSnapshot())
	if err != nil {
		t.Fatalf("EncodeOTLPJSON: unexpected error: %v", err)
	}
	if got := string(doc); got != otlpGolden {
		t.Errorf("encoded document does not match the hand-written schema rendering:\n got: %s\nwant: %s", got, otlpGolden)
	}
}

// TestEncodeOTLPJSONIsDeterministic pins the property an exporter's tests rely
// on: the same snapshot renders identically twice, although three Go maps were
// walked to produce it.
func TestEncodeOTLPJSONIsDeterministic(t *testing.T) {
	t.Parallel()
	snap := otlpFullSnapshot()
	first, err := svcmetrics.EncodeOTLPJSON(snap)
	if err != nil {
		t.Fatalf("EncodeOTLPJSON: unexpected error: %v", err)
	}
	//: ten passes is enough for Go's randomised map iteration to disagree with
	//: itself if anything downstream of the maps were unordered.
	for i := range 10 {
		again, againErr := svcmetrics.EncodeOTLPJSON(snap)
		if againErr != nil {
			t.Fatalf("EncodeOTLPJSON pass %d: unexpected error: %v", i, againErr)
		}
		if string(again) != string(first) {
			t.Fatalf("pass %d differs:\n got: %s\nwant: %s", i, again, first)
		}
	}
}

// TestEncodeOTLPJSONSpellsEveryAttributeKind pins the AnyValue oneof against
// common.proto: string_value = 1, bool_value = 2, int_value = 3 (int64, hence a
// decimal string), double_value = 4 (a double, hence a number).
func TestEncodeOTLPJSONSpellsEveryAttributeKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		attr coremetrics.AttrValue
		want string
	}{
		{"string", coremetrics.String("k", "v"), `{"key":"k","value":{"stringValue":"v"}}`},
		{"bool true", coremetrics.Bool("k", true), `{"key":"k","value":{"boolValue":true}}`},
		{"bool false", coremetrics.Bool("k", false), `{"key":"k","value":{"boolValue":false}}`},
		{"int64", coremetrics.Int64("k", -9007199254740993), `{"key":"k","value":{"intValue":"-9007199254740993"}}`},
		{"float64", coremetrics.Float64("k", 2.5), `{"key":"k","value":{"doubleValue":2.5}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			doc := mustEncode(t, gaugeSnapshot([]coremetrics.AttrValue{test.attr}, 1))
			if !strings.Contains(doc, test.want) {
				t.Errorf("attribute rendering missing\n got: %s\nwant substring: %s", doc, test.want)
			}
		})
	}
}

// TestEncodeOTLPJSONKeepsA64BitIntegerExact is why the specification demands
// decimal strings at all: 2^53+1 is not representable as a JSON number in a
// parser that reads numbers as doubles, and would silently come back as 2^53.
func TestEncodeOTLPJSONKeepsA64BitIntegerExact(t *testing.T) {
	t.Parallel()
	snap := coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
		"big": {
			Temporality: coremetrics.TemporalityCumulative,
			Monotonic:   true,
			Points:      []coremetrics.SumValue{{Value: math.MaxInt64}},
		},
	}}
	doc := mustEncode(t, snap)
	if !strings.Contains(doc, `"asInt":"9223372036854775807"`) {
		t.Errorf("a 64-bit sum must ride as an exact decimal string, got: %s", doc)
	}
}

// TestEncodeOTLPJSONNamesNonFiniteDoubles pins the proto3 JSON mapping for a
// double: "a number or one of the special string values 'NaN', 'Infinity', and
// '-Infinity'". encoding/json refuses all three outright, so without the
// mapping a single NaN reading would fail an entire export.
func TestEncodeOTLPJSONNamesNonFiniteDoubles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value float64
		want  string
	}{
		{"NaN", math.NaN(), `"asDouble":"NaN"`},
		{"positive infinity", math.Inf(1), `"asDouble":"Infinity"`},
		{"negative infinity", math.Inf(-1), `"asDouble":"-Infinity"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			doc := mustEncode(t, gaugeSnapshot(nil, test.value))
			if !strings.Contains(doc, test.want) {
				t.Errorf("non-finite double not named\n got: %s\nwant substring: %s", doc, test.want)
			}
		})
	}
}

// TestEncodeOTLPJSONRefusesAnUnresolvedTemporality pins the refusal the schema
// asks for in so many words: "UNSPECIFIED is the default AggregationTemporality,
// it MUST not be used".
func TestEncodeOTLPJSONRefusesAnUnresolvedTemporality(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		snap coremetrics.SnapshotValue
	}{
		{"sum", coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
			"s": {Temporality: coremetrics.TemporalityUnspecified, Points: []coremetrics.SumValue{{Value: 1}}},
		}}},
		{"histogram", coremetrics.SnapshotValue{Histograms: map[string]coremetrics.HistogramMetricValue{
			"h": {Temporality: coremetrics.TemporalityUnspecified, Points: []coremetrics.HistogramValue{{Counts: []uint64{1}}}},
		}}},
		{"cast", coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
			"s": {Temporality: coremetrics.Temporality(7), Points: []coremetrics.SumValue{{Value: 1}}},
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			doc, err := svcmetrics.EncodeOTLPJSON(test.snap)
			if !errors.Is(err, svcmetrics.OTLPUnresolvedTemporality) {
				t.Fatalf("want OTLPUnresolvedTemporality, got %v", err)
			}
			if doc != nil {
				t.Errorf("a refusal must produce no bytes, got: %s", doc)
			}
			if !kerrs.HasCode(err, svcmetrics.CodeOTLPUnresolvedTemporality) {
				t.Errorf("want code 0.3.45.5 on the trail, got %v", err)
			}
		})
	}
}

// TestEncodeOTLPJSONRefusesABucketLayoutOTLPCannotExpress pins the two
// invariants metrics.proto states for an explicit-bucket histogram: bucket
// counts one longer than the bounds, and bounds strictly increasing. A
// non-finite bound violates the second by construction, because the bucket
// above the last declared bound is already the +Inf overflow.
func TestEncodeOTLPJSONRefusesABucketLayoutOTLPCannotExpress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		point coremetrics.HistogramValue
	}{
		{"counts too short", coremetrics.HistogramValue{Bounds: []float64{1, 2}, Counts: []uint64{1, 2}}},
		{"counts too long", coremetrics.HistogramValue{Bounds: []float64{1}, Counts: []uint64{1, 2, 3}}},
		{"repeated bound", coremetrics.HistogramValue{Bounds: []float64{1, 1}, Counts: []uint64{1, 2, 3}}},
		{"descending bound", coremetrics.HistogramValue{Bounds: []float64{2, 1}, Counts: []uint64{1, 2, 3}}},
		{"infinite bound", coremetrics.HistogramValue{Bounds: []float64{1, math.Inf(1)}, Counts: []uint64{1, 2, 3}}},
		{"NaN bound", coremetrics.HistogramValue{Bounds: []float64{math.NaN()}, Counts: []uint64{1, 2}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			snap := coremetrics.SnapshotValue{Histograms: map[string]coremetrics.HistogramMetricValue{
				"h": {Temporality: coremetrics.TemporalityCumulative, Points: []coremetrics.HistogramValue{test.point}},
			}}
			doc, err := svcmetrics.EncodeOTLPJSON(snap)
			if !errors.Is(err, svcmetrics.OTLPInvalidBucketLayout) {
				t.Fatalf("want OTLPInvalidBucketLayout, got %v", err)
			}
			if doc != nil {
				t.Errorf("a refusal must produce no bytes, got: %s", doc)
			}
		})
	}
}

// TestEncodeOTLPJSONAcceptsABoundlessHistogram pins the degenerate but legal
// layout: no declared bound at all is one bucket, the implicit +Inf overflow.
// explicit_bounds is then an empty repeated field, which proto3 JSON omits.
func TestEncodeOTLPJSONAcceptsABoundlessHistogram(t *testing.T) {
	t.Parallel()
	snap := coremetrics.SnapshotValue{Histograms: map[string]coremetrics.HistogramMetricValue{
		"h": {
			Temporality: coremetrics.TemporalityCumulative,
			Points:      []coremetrics.HistogramValue{{Counts: []uint64{4}, Sum: 0, Count: 4}},
		},
	}}
	doc := mustEncode(t, snap)
	if !strings.Contains(doc, `"count":"4","sum":0,"bucketCounts":["4"]`) {
		t.Errorf("boundless histogram mis-rendered: %s", doc)
	}
	if strings.Contains(doc, "explicitBounds") {
		t.Errorf("an empty repeated field must be omitted: %s", doc)
	}
}

// TestEncodeOTLPJSONEmitsAZeroHistogramSum pins the consequence of sum being
// declared `optional double`: it has explicit presence, so an emitted 0 means
// "the observations summed to zero" and omitting it would mean "no sum was
// recorded". This SDK always has one.
func TestEncodeOTLPJSONEmitsAZeroHistogramSum(t *testing.T) {
	t.Parallel()
	snap := coremetrics.SnapshotValue{Histograms: map[string]coremetrics.HistogramMetricValue{
		"h": {
			Temporality: coremetrics.TemporalityDelta,
			Points:      []coremetrics.HistogramValue{{Counts: []uint64{2}, Sum: 0, Count: 2}},
		},
	}}
	doc := mustEncode(t, snap)
	if !strings.Contains(doc, `"sum":0`) {
		t.Errorf("an optional field this SDK always has must be emitted at zero: %s", doc)
	}
}

// TestEncodeOTLPJSONMapsAnUnsetWindowToZero pins the guard around
// time.Time.UnixNano, whose result is documented as undefined for the zero
// Time: it returns -6795364578871345152, which cast to a uint64 nanosecond
// timestamp reads as the year 2339. Zero is what the schema means by an unknown
// timestamp; a plausible wrong date is what a dashboard cannot detect.
func TestEncodeOTLPJSONMapsAnUnsetWindowToZero(t *testing.T) {
	t.Parallel()
	snap := coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
		"s": {
			Temporality: coremetrics.TemporalityCumulative,
			Monotonic:   true,
			Points:      []coremetrics.SumValue{{Value: 1}},
		},
	}}
	doc := mustEncode(t, snap)
	if !strings.Contains(doc, `"startTimeUnixNano":"0","timeUnixNano":"0"`) {
		t.Errorf("an unset window must encode as 0, got: %s", doc)
	}
	if strings.Contains(doc, "11651379494838206464") {
		t.Errorf("the undefined UnixNano of a zero Time reached the wire: %s", doc)
	}
}

// TestEncodeOTLPJSONDoesNotEscapeHTML pins the escaping switch. encoding/json
// turns '&' into & by default, a defence for JSON embedded in a <script>
// element; an OTLP body never is, and OTel-conventional attributes carry URLs
// whose query separator is exactly '&'.
func TestEncodeOTLPJSONDoesNotEscapeHTML(t *testing.T) {
	t.Parallel()
	raw := "https://example.test/search?q=a&lang=<go>"
	doc := mustEncode(t, gaugeSnapshot([]coremetrics.AttrValue{coremetrics.String("url.full", raw)}, 1))
	if !strings.Contains(doc, raw) {
		t.Errorf("URL attribute was escaped: %s", doc)
	}
}

// TestEncodeOTLPJSONOmitsTheDimensionlessAttributeArray pins the proto3 rule
// for an empty repeated field, which is what makes len(Attrs) == 0 the "no
// attributes" case rather than something each exporter special-cases.
func TestEncodeOTLPJSONOmitsTheDimensionlessAttributeArray(t *testing.T) {
	t.Parallel()
	doc := mustEncode(t, gaugeSnapshot(nil, 1))
	if strings.Contains(doc, `"attributes":[]`) {
		t.Errorf("an empty attribute set must be omitted, not emitted empty: %s", doc)
	}
}

// TestEncodeOTLPJSONOmitsAnAbsentScopeVersion pins the one optional identity
// field: version is optional in the specification, and an empty one says
// nothing, so it is absent rather than blank.
func TestEncodeOTLPJSONOmitsAnAbsentScopeVersion(t *testing.T) {
	t.Parallel()
	snap := gaugeSnapshot(nil, 1)
	snap.Scope = coremetrics.ScopeValue{Name: "lib"}
	doc := mustEncode(t, snap)
	if !strings.Contains(doc, `"scope":{"name":"lib"}`) {
		t.Errorf("scope rendering wrong: %s", doc)
	}
}

// TestNewOTLPJSONExporterWritesNDJSON pins the one difference between the
// writer-bound exporter and the encoder: a stream of documents is newline
// delimited, while an HTTP body is one document with no terminator.
func TestNewOTLPJSONExporterWritesNDJSON(t *testing.T) {
	t.Parallel()
	var sink bytes.Buffer
	exporter := svcmetrics.NewOTLPJSONExporter("otlp-test", &sink)
	snap := gaugeSnapshot(nil, 1)
	for range 2 {
		if err := exporter.Export(snap); err != nil {
			t.Fatalf("Export: unexpected error: %v", err)
		}
	}
	doc := mustEncode(t, snap)
	if got, want := sink.String(), doc+"\n"+doc+"\n"; got != want {
		t.Errorf("stream mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestNewOTLPJSONExporterRefusesBeforeWriting pins that a refusal leaves the
// writer untouched: a truncated OTLP document is not a document, and a receiver
// would reject the whole request rather than the series that could not be
// spelled.
func TestNewOTLPJSONExporterRefusesBeforeWriting(t *testing.T) {
	t.Parallel()
	var sink bytes.Buffer
	exporter := svcmetrics.NewOTLPJSONExporter("otlp-test", &sink)
	snap := coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
		"s": {Temporality: coremetrics.TemporalityUnspecified, Points: []coremetrics.SumValue{{Value: 1}}},
	}}
	if err := exporter.Export(snap); !errors.Is(err, svcmetrics.OTLPUnresolvedTemporality) {
		t.Fatalf("want OTLPUnresolvedTemporality, got %v", err)
	}
	if sink.Len() != 0 {
		t.Errorf("a refusal must leave the writer untouched, got: %s", sink.String())
	}
}

// TestOTLPJSONExporterReportsAWriterFault pins the remaining error surface: the
// single write.
func TestOTLPJSONExporterReportsAWriterFault(t *testing.T) {
	t.Parallel()
	boom := errors.New("pipe closed")
	exporter := svcmetrics.NewOTLPJSONExporter("otlp-test", failingWriter{err: boom})
	err := exporter.Export(gaugeSnapshot(nil, 1))
	if !errors.Is(err, boom) {
		t.Fatalf("want the writer's cause on the trail, got %v", err)
	}
	if !kerrs.HasCode(err, coremetrics.CodeExportFailed) {
		t.Errorf("want EXPORT_FAILED (0.2.9.2), got %v", err)
	}
}

// TestOTLPJSONIsRegisteredOnStderr pins ADR 0030 for the third exporter: the
// registered default must not be armed on a stream the process may be using as
// a protocol channel, and it must be reachable through the registry.
func TestOTLPJSONIsRegisteredOnStderr(t *testing.T) {
	t.Parallel()
	exporter, ok := coremetrics.LookupExporter("otlpjson")
	if !ok {
		t.Fatalf("the otlpjson exporter must self-register on import")
	}
	if exporter != svcmetrics.OTLPJSON {
		t.Errorf("the registry must hold the package's own singleton")
	}
	if exporter.Name() != coremetrics.ExporterName("otlpjson") {
		t.Errorf("registered name mismatch: %q", exporter.Name())
	}
}

// gaugeSnapshot is the smallest snapshot that carries one attributed gauge
// point: no temporality to resolve and no bucket ladder to validate, so it
// isolates whatever the caller is actually asserting on.
func gaugeSnapshot(attrs []coremetrics.AttrValue, value float64) coremetrics.SnapshotValue {
	return coremetrics.SnapshotValue{
		StartTime: otlpStart,
		Time:      otlpEnd,
		Gauges: map[string]coremetrics.GaugeMetricValue{
			"g": {Points: []coremetrics.GaugeValue{{Attrs: attrs, Value: value}}},
		},
	}
}

// mustEncode encodes snap or fails the test.
func mustEncode(t *testing.T, snap coremetrics.SnapshotValue) string {
	t.Helper()
	doc, err := svcmetrics.EncodeOTLPJSON(snap)
	if err != nil {
		t.Fatalf("EncodeOTLPJSON: unexpected error: %v", err)
	}
	return string(doc)
}
