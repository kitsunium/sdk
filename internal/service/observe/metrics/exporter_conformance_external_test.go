// Package metrics_test — the conformance table of the exporters
// (kitsunium/sdk#1123): every rule the four exporters — text, Prometheus,
// OTLP/JSON over a writer, OTLP/HTTP through a collector — must share, written
// once and run over each, the exporter's name in the subtest's. A rule added
// later reaches the four at once. What differs — the grammar a description is
// written in, the code a dead sink is reported under — is each exporter's
// entry in everyExporter; what belongs to one grammar alone (Prometheus' HELP
// before TYPE, OTLP's field order) stays in that exporter's file.
//
// The rules cover every kind a snapshot holds, the exponential histogram
// (#869) included, and every field a point or an envelope carries: the unit
// (ADR 0208, #1021) where a grammar has a field for one and byte-for-byte
// nothing where it has none; the exemplars (#869) the same way; the sum a
// NoSum histogram (#868) does not have; and the default ladder of a histogram
// created with no buckets (#1135).
package metrics_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// exporterUnderTest is one exporter as the table drives it.
type exporterUnderTest struct {
	name string
	// open returns the exporter over a sink, and a read of everything the
	// sink received so far.
	open func(t *testing.T) (coremetrics.Exporter, func() string)
	// broken returns the exporter over a sink that fails, and the cause the
	// failure must keep reachable (nil when the sink's cause is the
	// exporter's own sentinel, as a hung-up collector's is).
	broken func(t *testing.T) (coremetrics.Exporter, error)
	// sinkCode is the code a dead sink is reported under.
	sinkCode kerrs.Code
	// describes is how the exporter's grammar writes metric's description: the
	// exact text a document carrying help must contain.
	describes func(metric, help string) string
	// mentions is the token any description in the grammar starts with; an
	// undescribed document must not contain it.
	mentions string
	// bare is how the grammar opens an undescribed metric, the shape a
	// description left out must leave unchanged.
	bare func(metric string) string
	// escaped is how the grammar writes escapeHelp as requests_total's
	// description: what it escapes, and what it leaves.
	escaped string
	// refused is a snapshot the exporter refuses whole, or nil when it renders
	// every snapshot — the text exporter, whose job is the whole model.
	refused *coremetrics.SnapshotValue
	// units is how the grammar writes a described metric's unit, after its
	// description (ADR 0208); nil when the grammar has no field for one — the
	// Prometheus exposition, whose document must then be the same bytes with
	// and without a unit: the loss is named, not silent.
	units func(metric, help, unit string) string
	// unitMentions is the token any unit in the grammar starts with; a
	// document nobody declared a unit for must not contain it.
	unitMentions string
	// exemplars is true when the grammar has a field for an exemplar (OTLP's
	// exemplars); a grammar with none writes the same bytes with and without
	// one (#869).
	exemplars bool
	// exponential is how the grammar writes exponentialPoint as request_size,
	// every fragment in the document: OTLP's exponentialHistogram, the family
	// of totals Prometheus keeps, the text exporter's count.
	exponential []string
	// sumOf is the token the grammar writes a histogram's sum with, or nil
	// when it writes none for any histogram (the text exporter prints the
	// count): a histogram whose source keeps no sum (NoSum) must not carry it.
	sumOf func(metric string) string
	// ladder is how the grammar writes a histogram created with no buckets
	// that holds one observation of ladderObservation: the whole default
	// ladder (#1135) — or the count, in the text grammar, which writes no
	// ladder for any layout.
	ladder func(metric string) string
}

// unresolvedTemporality is a snapshot no OTLP document can spell: a sum with
// no temporality.
var unresolvedTemporality = coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
	"s": {Temporality: coremetrics.TemporalityUnspecified, Points: []coremetrics.SumValue{{Value: 1}}},
}}

// unspellableName is a snapshot the Prometheus format cannot carry: a metric
// name with a character the exposition grammar refuses.
var unspellableName = coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
	"requests total": {Temporality: coremetrics.TemporalityCumulative, Monotonic: true, Points: []coremetrics.SumValue{{Value: 1}}},
}}

// bufferSink is a writer the table reads back, safe for an exporter's lock.
type bufferSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write implements io.Writer.
func (s *bufferSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// String returns everything written so far.
func (s *bufferSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// writerExporter adapts a writer-bound constructor to the table.
func writerExporter(build func(name coremetrics.ExporterName, sink *bufferSink) coremetrics.Exporter) func(t *testing.T) (coremetrics.Exporter, func() string) {
	return func(t *testing.T) (coremetrics.Exporter, func() string) {
		t.Helper()
		sink := &bufferSink{}
		return build("conformance", sink), sink.String
	}
}

// collector starts a collector stub that keeps every body it receives, and
// returns the OTLP/HTTP exporter pointed at it.
func collector(t *testing.T) (coremetrics.Exporter, func() string) {
	t.Helper()
	var mu sync.Mutex
	var bodies strings.Builder
	captured := &capturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.record(r)
		mu.Lock()
		bodies.WriteString(captured.snapshot().body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	exporter, err := svcmetrics.NewOTLPHTTPExporter("conformance", svcmetrics.OTLPHTTPConfig{
		Endpoint: server.URL + svcmetrics.OTLPMetricsPath,
	})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	return exporter, func() string {
		mu.Lock()
		defer mu.Unlock()
		return bodies.String()
	}
}

// deadCollector returns the OTLP/HTTP exporter pointed at a collector that
// hangs up on every request: its sink, dead.
func deadCollector(t *testing.T) (coremetrics.Exporter, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hangUp(w) }))
	t.Cleanup(server.Close)
	exporter, err := svcmetrics.NewOTLPHTTPExporter("conformance", svcmetrics.OTLPHTTPConfig{
		Endpoint: server.URL + svcmetrics.OTLPMetricsPath,
	})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	return exporter, nil
}

// errSinkGone is the dead writer's cause.
var errSinkGone = errors.New("the sink is gone")

// failingSink returns a writer exporter over a writer that always fails.
func failingSink(build func(name coremetrics.ExporterName, sink failingWriter) coremetrics.Exporter) func(t *testing.T) (coremetrics.Exporter, error) {
	return func(*testing.T) (coremetrics.Exporter, error) {
		return build("conformance", failingWriter{err: errSinkGone}), errSinkGone
	}
}

// otlpDescribes is the OTLP/JSON grammar's description: a JSON string field,
// second after the name.
func otlpDescribes(metric, help string) string {
	return `{"name":"` + metric + `","description":"` + help + `",`
}

// everyExporter returns the four exporters the table runs over.
func everyExporter() []exporterUnderTest {
	return []exporterUnderTest{
		{
			name: "text",
			open: writerExporter(func(n coremetrics.ExporterName, s *bufferSink) coremetrics.Exporter {
				return svcmetrics.NewTextExporter(n, s)
			}),
			broken: failingSink(func(n coremetrics.ExporterName, s failingWriter) coremetrics.Exporter {
				return svcmetrics.NewTextExporter(n, s)
			}),
			sinkCode: coremetrics.CodeExportFailed,
			//: the text grammar QUOTES the description, after the qualifiers.
			describes: func(metric, help string) string {
				return metric + textQualifiers[metric] + ` description="` + help + `"` + "\n"
			},
			mentions: "description=",
			bare:     func(metric string) string { return "# metric " + metric + textQualifiers[metric] + "\n" },
			//: quoted, so the quote is escaped too.
			escaped: `description="Requests to the \"public\" endpoint\nevil_metric 999 at C:\\temp"`,
			//: quoted too, after the description.
			units: func(metric, help, unit string) string {
				return metric + textQualifiers[metric] + ` description="` + help + `" unit="` + unit + `"` + "\n"
			},
			unitMentions: "unit=",
			exponential:  []string{"# metric request_size exponential_histogram cumulative\nrequest_size 6\n"},
			//: the count alone, for any layout: no sum, no ladder.
			ladder: func(metric string) string { return "# metric " + metric + " histogram cumulative\n" + metric + " 1\n" },
		},
		{
			name: "prometheus",
			open: writerExporter(func(n coremetrics.ExporterName, s *bufferSink) coremetrics.Exporter {
				return svcmetrics.NewPrometheusExporter(n, s)
			}),
			broken: failingSink(func(n coremetrics.ExporterName, s failingWriter) coremetrics.Exporter {
				return svcmetrics.NewPrometheusExporter(n, s)
			}),
			sinkCode: coremetrics.CodeExportFailed,
			//: the exposition grammar's docstring: unquoted, its own line.
			describes: func(metric, help string) string { return "# HELP " + metric + " " + help + "\n" },
			mentions:  "# HELP",
			bare:      func(metric string) string { return "# TYPE " + metric + " " },
			//: unquoted, so the quote is NOT escaped: the format escapes only
			//: the backslash and the newline in a docstring.
			escaped: `# HELP requests_total Requests to the "public" endpoint\nevil_metric 999 at C:\\temp` + "\n",
			refused: &unspellableName,
			//: no units: no suffix and no # UNIT line (ADR 0208).
			unitMentions: "# UNIT",
			//: the totals: a native histogram's buckets are the protobuf
			//: exposition's, never spelled as classic le buckets.
			exponential: []string{"# TYPE request_size histogram\n" +
				`request_size_bucket{le="+Inf"} 6` + "\n" +
				"request_size_sum 7.5\n" +
				"request_size_count 6\n"},
			sumOf:  func(metric string) string { return "\n" + metric + "_sum" },
			ladder: promLadder,
		},
		{
			name: "otlpjson",
			open: writerExporter(func(n coremetrics.ExporterName, s *bufferSink) coremetrics.Exporter {
				return svcmetrics.NewOTLPJSONExporter(n, s)
			}),
			broken: failingSink(func(n coremetrics.ExporterName, s failingWriter) coremetrics.Exporter {
				return svcmetrics.NewOTLPJSONExporter(n, s)
			}),
			sinkCode:     coremetrics.CodeExportFailed,
			describes:    otlpDescribes,
			mentions:     `"description"`,
			bare:         otlpBare,
			escaped:      otlpEscaped,
			refused:      &unresolvedTemporality,
			units:        otlpUnits,
			unitMentions: `"unit"`,
			exemplars:    true,
			exponential:  otlpExponentialFragments,
			sumOf:        otlpSumOf,
			ladder:       otlpLadder,
		},
		{
			name:         "otlphttp",
			open:         collector,
			broken:       deadCollector,
			sinkCode:     coremetrics.CodeOTLPExportUnavailable,
			describes:    otlpDescribes,
			mentions:     `"description"`,
			bare:         otlpBare,
			escaped:      otlpEscaped,
			refused:      &unresolvedTemporality,
			units:        otlpUnits,
			unitMentions: `"unit"`,
			exemplars:    true,
			exponential:  otlpExponentialFragments,
			sumOf:        otlpSumOf,
			ladder:       otlpLadder,
		},
	}
}

// otlpBare is the OTLP/JSON grammar's undescribed metric: the data field
// right after the name, with no description key between them.
func otlpBare(metric string) string {
	return `{"name":"` + metric + `","`
}

// otlpEscaped is escapeHelp as a JSON string: the quote, the newline and the
// backslash all escaped.
const otlpEscaped = `"description":"Requests to the \"public\" endpoint\nevil_metric 999 at C:\\temp"`

// otlpUnits is the OTLP/JSON grammar's unit: a JSON string field right after
// the description.
func otlpUnits(metric, help, unit string) string {
	return otlpDescribes(metric, help) + `"unit":"` + unit + `",`
}

// otlpExponentialFragments is exponentialPoint as OTLP/JSON writes it under
// request_size: an exponentialHistogram metric, then the point's fields after
// its window.
var otlpExponentialFragments = []string{
	`{"name":"request_size","exponentialHistogram":{"dataPoints":[{`,
	`"count":"6","sum":7.5,"scale":2,"zeroCount":"1",` +
		`"positive":{"offset":-3,"bucketCounts":["2","0","1"]},` +
		`"negative":{"offset":4,"bucketCounts":["2"]}`,
}

// otlpSumOf is the OTLP/JSON grammar's histogram sum: the optional field
// after the count.
func otlpSumOf(string) string { return `"sum":` }

// ladderObservation is the one value the ladder row records: above the
// first default bound, at or under the second.
const ladderObservation = 0.3

// otlpLadder is a default-ladder histogram holding ladderObservation in
// OTLP/JSON: one count per bucket, then every bound of DefaultBuckets.
func otlpLadder(string) string {
	bounds := coremetrics.DefaultBuckets()
	counts := make([]string, 0, len(bounds)+1)
	written := make([]string, 0, len(bounds))
	for i, bound := range bounds {
		count := `"0"`
		//: the bucket (bounds[i-1], bounds[i]] the observation falls in.
		if bound >= ladderObservation && (i == 0 || bounds[i-1] < ladderObservation) {
			count = `"1"`
		}
		counts = append(counts, count)
		written = append(written, strconv.FormatFloat(bound, 'f', -1, 64))
	}
	counts = append(counts, `"0"`)
	return `"bucketCounts":[` + strings.Join(counts, ",") + `],"explicitBounds":[` + strings.Join(written, ",") + `]`
}

// promLadder is a default-ladder histogram holding ladderObservation in the
// exposition grammar: every bound of DefaultBuckets, cumulative, then +Inf.
func promLadder(metric string) string {
	var b strings.Builder
	b.WriteString("# TYPE " + metric + " histogram\n")
	for _, bound := range coremetrics.DefaultBuckets() {
		count := "1"
		//: cumulative: every bucket at or above the observation counts it.
		if bound < ladderObservation {
			count = "0"
		}
		b.WriteString(metric + `_bucket{le="` + strconv.FormatFloat(bound, 'g', -1, 64) + `"} ` + count + "\n")
	}
	b.WriteString(metric + `_bucket{le="+Inf"} 1` + "\n")
	return b.String()
}

// escapeHelp is the adversarial description: a quote, a newline that would
// forge a series, and a backslash that would eat the next character.
const escapeHelp = "Requests to the \"public\" endpoint\nevil_metric 999 at C:\\temp"

// textQualifiers are the qualifiers the text grammar writes between a
// metric's name and its description, per metric of describedSnapshot.
var textQualifiers = map[string]string{
	"requests_total":  " sum cumulative monotonic",
	"queue_depth":     " gauge",
	"latency_seconds": " histogram cumulative",
	"request_size":    " exponential_histogram cumulative",
}

// everyKind names the metric of each kind describedSnapshot holds: a sum, a
// gauge, an explicit and an exponential histogram.
var everyKind = []string{"requests_total", "queue_depth", "latency_seconds", "request_size"}

// exporterRow is one rule, run against one exporter.
type exporterRow struct {
	name string
	run  func(t *testing.T, e exporterUnderTest)
}

// exporterRows is the table.
func exporterRows() []exporterRow {
	return []exporterRow{
		{"a-description-is-rendered-for-every-kind", rowDescriptionRendered},
		{"an-absent-description-is-not-written", rowDescriptionOmitted},
		{"a-description-cannot-forge-a-line", rowDescriptionEscaped},
		{"a-dead-sink-is-reported-typed-with-its-cause", rowSinkFault},
		{"a-refusal-writes-nothing", rowRefusalWritesNothing},
		{"a-real-meter's-snapshot-is-accepted", rowRealMeter},
		{"a-unit-is-rendered-for-every-kind-or-dropped-whole", rowUnitRendered},
		{"an-absent-unit-is-not-written", rowUnitOmitted},
		{"a-unit-cannot-forge-a-line", rowUnitEscaped},
		{"an-exponential-histogram-is-rendered", rowExponentialRendered},
		{"exemplars-are-carried-or-dropped-whole", rowExemplars},
		{"a-histogram-without-a-sum-writes-none", rowNoSum},
		{"a-histogram-without-buckets-gets-the-default-ladder", rowDefaultLadder},
	}
}

// TestExporterConformance runs every row over every exporter.
func TestExporterConformance(t *testing.T) {
	t.Parallel()
	for _, e := range everyExporter() {
		for _, row := range exporterRows() {
			t.Run(e.name+"/"+row.name, func(t *testing.T) {
				t.Parallel()
				row.run(t, e)
			})
		}
	}
}

// export ships snap through a fresh exporter and returns what its sink got.
func export(t *testing.T, e exporterUnderTest, snap coremetrics.SnapshotValue) string {
	t.Helper()
	exporter, read := e.open(t)
	if err := exporter.Export(snap); err != nil {
		t.Fatalf("Export: %v", err)
	}
	return read()
}

// rowDescriptionRendered: a description reaches the document for a sum, a
// gauge, an explicit and an exponential histogram, written in the exporter's
// grammar (ADR 0067).
func rowDescriptionRendered(t *testing.T, e exporterUnderTest) {
	t.Helper()
	doc := export(t, e, describedSnapshot("Requests served"))
	for _, metric := range everyKind {
		if want := e.describes(metric, "Requests served"); !strings.Contains(doc, want) {
			t.Errorf("%s: the document does not carry %q:\n%s", metric, want, doc)
		}
	}
}

// rowDescriptionOmitted: a metric nobody described carries no description
// token at all — not an empty one.
func rowDescriptionOmitted(t *testing.T, e exporterUnderTest) {
	t.Helper()
	doc := export(t, e, describedSnapshot(""))
	if doc == "" {
		t.Fatal("the undescribed snapshot wrote nothing")
	}
	if strings.Contains(doc, e.mentions) {
		t.Errorf("an undescribed snapshot wrote %q:\n%s", e.mentions, doc)
	}
	for _, metric := range everyKind {
		if want := e.bare(metric); !strings.Contains(doc, want) {
			t.Errorf("%s: the undescribed metric is not opened by %q:\n%s", metric, want, doc)
		}
	}
}

// rowDescriptionEscaped: a description holding a newline, a backslash and a
// double quote is escaped as the exporter's grammar requires — the newline as
// the two characters \n in all four, so no forged line and no forged series
// follows it; the quote as each grammar quotes.
func rowDescriptionEscaped(t *testing.T, e exporterUnderTest) {
	t.Helper()
	metric := promCounter(coremetrics.SumValue{Value: 1})
	metric.Description = escapeHelp
	doc := export(t, e, coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{"requests_total": metric}})
	if strings.Contains(doc, "\nevil_metric") {
		t.Fatalf("the description forged a line:\n%s", doc)
	}
	if !strings.Contains(doc, e.escaped) {
		t.Errorf("the description is not written as %s:\n%s", e.escaped, doc)
	}
}

// rowSinkFault: a sink that fails is reported under the exporter's code, the
// sink's own cause kept on the chain — or an operator cannot tell a closed
// pipe from a full disk — for an empty snapshot and a populated one.
func rowSinkFault(t *testing.T, e exporterUnderTest) {
	t.Helper()
	for name, snap := range map[string]coremetrics.SnapshotValue{
		"empty":     {},
		"populated": gaugeSnapshot(nil, 1),
	} {
		exporter, cause := e.broken(t)
		err := exporter.Export(snap)
		if !kerrs.HasCode(err, e.sinkCode) {
			t.Fatalf("%s: Export = %v, want code %v", name, err, e.sinkCode)
		}
		if cause != nil && !errors.Is(err, cause) {
			t.Errorf("%s: Export = %v, want it to wrap %v", name, err, cause)
		}
	}
}

// rowRefusalWritesNothing: a snapshot the exporter cannot write whole is
// refused before anything reaches the sink — a truncated document is not a
// document. An exporter that refuses nothing renders even the snapshots the
// others refuse.
func rowRefusalWritesNothing(t *testing.T, e exporterUnderTest) {
	t.Helper()
	if e.refused == nil {
		for _, snap := range []coremetrics.SnapshotValue{unresolvedTemporality, unspellableName} {
			if doc := export(t, e, snap); doc == "" {
				t.Errorf("an exporter that refuses nothing wrote nothing")
			}
		}
		return
	}
	exporter, read := e.open(t)
	if err := exporter.Export(*e.refused); err == nil {
		t.Fatalf("Export accepted a snapshot it cannot write: %s", read())
	}
	if got := read(); got != "" {
		t.Errorf("a refusal reached the sink:\n%s", got)
	}
}

// rowRealMeter: what a real meter collects — a counter, a gauge, an explicit
// and an exponential histogram, one described and given a unit — is accepted
// and written.
func rowRealMeter(t *testing.T, e exporterUnderTest) {
	t.Helper()
	meter := svcmetrics.NewMeter()
	meter.Counter("requests_total").Add(3)
	meter.Gauge("queue_depth").Set(2)
	meter.Histogram("latency_seconds", []float64{0.1, 1}).Record(0.5)
	meter.(coremetrics.ExponentialMeter).ExponentialHistogram("request_size", 0).Record(512)
	meter.(coremetrics.Describer).Describe("requests_total", "Requests served")
	meter.(coremetrics.UnitDescriber).Unit("requests_total", "{request}")
	doc := export(t, e, meter.Collect())
	for _, metric := range everyKind {
		if !strings.Contains(doc, metric) {
			t.Errorf("the document does not name %s:\n%s", metric, doc)
		}
	}
	//: the description, and the unit after it where the grammar has one.
	want := e.describes("requests_total", "Requests served")
	if e.units != nil {
		want = e.units("requests_total", "Requests served", "{request}")
	}
	if !strings.Contains(doc, want) {
		t.Errorf("the described counter lost its description or its unit, want %q:\n%s", want, doc)
	}
}

// withUnit returns describedSnapshot(help) with unit declared on every
// metric of it.
func withUnit(help, unit string) coremetrics.SnapshotValue {
	snap := describedSnapshot(help)
	for name, metric := range snap.Sums {
		metric.Unit = unit
		snap.Sums[name] = metric
	}
	for name, metric := range snap.Gauges {
		metric.Unit = unit
		snap.Gauges[name] = metric
	}
	for name, metric := range snap.Histograms {
		metric.Unit = unit
		snap.Histograms[name] = metric
	}
	for name, metric := range snap.ExponentialHistograms {
		metric.Unit = unit
		snap.ExponentialHistograms[name] = metric
	}
	return snap
}

// rowUnitRendered: a unit declared on a metric of each kind reaches the
// document in the exporter's grammar, after the description (ADR 0208); a
// grammar with no field for one writes the same bytes as without it.
func rowUnitRendered(t *testing.T, e exporterUnderTest) {
	t.Helper()
	doc := export(t, e, withUnit("Requests served", "s"))
	if e.units == nil {
		if bare := export(t, e, describedSnapshot("Requests served")); doc != bare {
			t.Fatalf("a unit changed a document that has no field for one:\n%s\nvs\n%s", doc, bare)
		}
		return
	}
	for _, metric := range everyKind {
		if want := e.units(metric, "Requests served", "s"); !strings.Contains(doc, want) {
			t.Errorf("%s: the document does not carry %q:\n%s", metric, want, doc)
		}
	}
}

// rowUnitOmitted: a metric nobody gave a unit carries no unit token at all —
// not an empty one.
func rowUnitOmitted(t *testing.T, e exporterUnderTest) {
	t.Helper()
	doc := export(t, e, describedSnapshot("Requests served"))
	if strings.Contains(doc, e.unitMentions) {
		t.Errorf("a snapshot with no unit wrote %q:\n%s", e.unitMentions, doc)
	}
}

// rowUnitEscaped: a unit the meter would refuse but a snapshot built by hand
// can hold — a quote and a newline — is escaped as the grammar escapes a
// description, or dropped: never a forged line.
func rowUnitEscaped(t *testing.T, e exporterUnderTest) {
	t.Helper()
	doc := export(t, e, withUnit("", "s\"\nevil_metric 999"))
	if strings.Contains(doc, "\nevil_metric") {
		t.Fatalf("the unit forged a line:\n%s", doc)
	}
}

// exponentialOnly is a snapshot holding exponentialPoint under request_size
// alone, undescribed.
func exponentialOnly() coremetrics.SnapshotValue {
	return coremetrics.SnapshotValue{ExponentialHistograms: map[string]coremetrics.ExponentialHistogramMetricValue{
		"request_size": {Temporality: coremetrics.TemporalityCumulative, Points: []coremetrics.ExponentialHistogramValue{exponentialPoint}},
	}}
}

// rowExponentialRendered: an exponential histogram is written in the
// exporter's grammar — whole where the grammar has a type for it, as the
// totals it can carry where it has not (#869).
func rowExponentialRendered(t *testing.T, e exporterUnderTest) {
	t.Helper()
	doc := export(t, e, exponentialOnly())
	for _, want := range e.exponential {
		if !strings.Contains(doc, want) {
			t.Errorf("the document does not carry %q:\n%s", want, doc)
		}
	}
}

// exemplarSnapshot is a sum, an explicit and an exponential histogram, each
// point holding one exemplar when with is true.
func exemplarSnapshot(with bool) coremetrics.SnapshotValue {
	var kept []coremetrics.ExemplarValue
	if with {
		kept = []coremetrics.ExemplarValue{{Time: exAt, Value: 1, TraceID: exTraceID, SpanID: exSpanID}}
	}
	exponential := exponentialPoint
	exponential.Exemplars = kept
	return coremetrics.SnapshotValue{
		Sums: map[string]coremetrics.SumMetricValue{"requests_total": {
			Temporality: coremetrics.TemporalityCumulative, Monotonic: true,
			Points: []coremetrics.SumValue{{Value: 1, Exemplars: kept}},
		}},
		Histograms: map[string]coremetrics.HistogramMetricValue{"latency_seconds": {
			Temporality: coremetrics.TemporalityCumulative,
			Points:      []coremetrics.HistogramValue{{Bounds: []float64{1}, Counts: []uint64{1, 0}, Sum: 1, Count: 1, Exemplars: kept}},
		}},
		ExponentialHistograms: map[string]coremetrics.ExponentialHistogramMetricValue{"request_size": {
			Temporality: coremetrics.TemporalityCumulative,
			Points:      []coremetrics.ExponentialHistogramValue{exponential},
		}},
	}
}

// rowExemplars: a grammar with a field for an exemplar carries each one,
// its trace and span; a grammar with none writes the same bytes with and
// without them (#869).
func rowExemplars(t *testing.T, e exporterUnderTest) {
	t.Helper()
	doc, bare := export(t, e, exemplarSnapshot(true)), export(t, e, exemplarSnapshot(false))
	if !e.exemplars {
		if doc != bare {
			t.Fatalf("an exemplar changed a document that has no field for one:\n%s\nvs\n%s", doc, bare)
		}
		return
	}
	trace := `"traceId":"` + hex.EncodeToString(exTraceID[:]) + `"`
	span := `"spanId":"` + hex.EncodeToString(exSpanID[:]) + `"`
	if strings.Count(doc, trace) != 3 || strings.Count(doc, span) != 3 {
		t.Errorf("want the three exemplars, each with its trace and span:\n%s", doc)
	}
	if strings.Contains(bare, `"exemplars"`) {
		t.Errorf("a point with no exemplar wrote the field:\n%s", bare)
	}
}

// noSumHistogram is latency_seconds alone, keeping its sum or not.
func noSumHistogram(noSum bool) coremetrics.SnapshotValue {
	return coremetrics.SnapshotValue{Histograms: map[string]coremetrics.HistogramMetricValue{"latency_seconds": {
		Temporality: coremetrics.TemporalityCumulative,
		Points:      []coremetrics.HistogramValue{{Bounds: []float64{1}, Counts: []uint64{2, 1}, Sum: 4, Count: 3, NoSum: noSum}},
	}}}
}

// rowNoSum: a histogram whose source keeps no sum — the runtime's — carries
// none, never a forged one: OTLP leaves the optional field out, Prometheus
// writes no _sum line (#868); a grammar that prints no sum for any histogram
// is unchanged.
func rowNoSum(t *testing.T, e exporterUnderTest) {
	t.Helper()
	doc, summed := export(t, e, noSumHistogram(true)), export(t, e, noSumHistogram(false))
	if e.sumOf == nil {
		if doc != summed {
			t.Fatalf("NoSum changed a document that prints no sum:\n%s\nvs\n%s", doc, summed)
		}
		return
	}
	token := e.sumOf("latency_seconds")
	if !strings.Contains(summed, token) {
		t.Fatalf("the histogram that keeps its sum does not write %q:\n%s", token, summed)
	}
	if strings.Contains(doc, token) {
		t.Errorf("a histogram with no sum wrote %q:\n%s", token, doc)
	}
}

// rowDefaultLadder: a histogram created with no buckets records into
// DefaultBuckets, and the exporter writes that ladder whole (#1135) — never
// the single +Inf bucket a nil layout once was.
func rowDefaultLadder(t *testing.T, e exporterUnderTest) {
	t.Helper()
	meter := svcmetrics.NewMeter()
	meter.Histogram("latency_seconds", nil).Record(ladderObservation)
	doc := export(t, e, meter.Collect())
	if want := e.ladder("latency_seconds"); !strings.Contains(doc, want) {
		t.Errorf("the document does not carry\n%s\ngot:\n%s", want, doc)
	}
}
