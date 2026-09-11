// Package metrics_test — the Prometheus exporter as a consumer wires it up.
package metrics_test

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcmetrics "github.com/kitsunium/sdk/internal/service/metrics"
)

// labelsOf is the one-label shorthand the tables below share.
func labelsOf(pairs ...string) []coremetrics.LabelValue {
	out := make([]coremetrics.LabelValue, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, coremetrics.LabelValue{Key: pairs[i], Value: pairs[i+1]})
	}
	return out
}

// exportPrometheus renders snap through a fresh exporter over a buffer.
func exportPrometheus(t *testing.T, snap coremetrics.SnapshotValue) string {
	t.Helper()
	var buf bytes.Buffer
	if err := svcmetrics.NewPrometheusExporter("test", &buf).Export(snap); err != nil {
		t.Fatalf("Export = %v, want nil", err)
	}
	return buf.String()
}

// TestPrometheusExporterFormat pins the rendered document against the exposition
// format, one shape per case.
//
// The histogram case is not invented: it reproduces the
// `http_request_duration_seconds` family published in the Prometheus exposition
// format documentation, byte for byte apart from the HELP line this exporter
// deliberately omits. Its per-bucket counts are the published CUMULATIVE ones
// differenced back into the per-bucket form the meter actually stores, so the
// test fails if the cumulative conversion is ever dropped.
func TestPrometheusExporterFormat(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		snap coremetrics.SnapshotValue
		want string
	}
	tests := []tc{
		{name: "an empty snapshot renders nothing at all", want: ""},
		{
			name: "a dimensionless counter renders as a bare name",
			snap: coremetrics.SnapshotValue{Counters: map[string][]coremetrics.CounterValue{
				"requests_total": {{Value: 7}},
			}},
			want: "# TYPE requests_total counter\nrequests_total 7\n",
		},
		{
			//: the header is written ONCE, then every series under it — the
			//: property the name-keyed snapshot shape exists to make free.
			name: "one header, several series",
			snap: coremetrics.SnapshotValue{Counters: map[string][]coremetrics.CounterValue{
				"requests_total": {
					{Labels: labelsOf("method", "GET"), Value: 7},
					{Labels: labelsOf("method", "POST"), Value: 2},
				},
			}},
			want: "# TYPE requests_total counter\n" +
				"requests_total{method=\"GET\"} 7\n" +
				"requests_total{method=\"POST\"} 2\n",
		},
		{
			name: "several labels stay in the snapshot's canonical order",
			snap: coremetrics.SnapshotValue{Counters: map[string][]coremetrics.CounterValue{
				"requests_total": {{Labels: labelsOf("code", "200", "method", "post"), Value: 1027}},
			}},
			want: "# TYPE requests_total counter\n" +
				"requests_total{code=\"200\",method=\"post\"} 1027\n",
		},
		{
			//: declared out of order; the document must be sorted by name.
			name: "families are sorted by name",
			snap: coremetrics.SnapshotValue{Counters: map[string][]coremetrics.CounterValue{
				"z": {{Value: 1}}, "a": {{Value: 2}},
			}},
			want: "# TYPE a counter\na 2\n# TYPE z counter\nz 1\n",
		},
		{
			//: a colon is legal in a metric name — it is what recording rules
			//: use — and illegal in a label name, which the refusal table pins.
			name: "a colon is legal in a metric name",
			snap: coremetrics.SnapshotValue{Gauges: map[string][]coremetrics.GaugeValue{
				"job:rate:5m": {{Value: 2.5}},
			}},
			want: "# TYPE job:rate:5m gauge\njob:rate:5m 2.5\n",
		},
		{
			//: the format names NaN / +Inf / -Inf as valid values, and Go's
			//: own FormatFloat spells all three exactly that way.
			name: "a gauge spells the non-finite values the format names",
			snap: coremetrics.SnapshotValue{Gauges: map[string][]coremetrics.GaugeValue{
				"a_nan": {{Value: math.NaN()}},
				"b_pos": {{Value: math.Inf(1)}},
				"c_neg": {{Value: math.Inf(-1)}},
			}},
			want: "# TYPE a_nan gauge\na_nan NaN\n" +
				"# TYPE b_pos gauge\nb_pos +Inf\n" +
				"# TYPE c_neg gauge\nc_neg -Inf\n",
		},
		{
			//: the published example from the exposition-format documentation,
			//: with the per-bucket counts the meter stores.
			name: "the documentation's own histogram family",
			snap: coremetrics.SnapshotValue{Histograms: map[string][]coremetrics.HistogramValue{
				"http_request_duration_seconds": {{
					Buckets: []float64{0.05, 0.1, 0.2, 0.5, 1},
					Counts:  []uint64{24054, 9390, 66948, 28997, 4599, 10332},
					Sum:     53423,
					Count:   144320,
				}},
			}},
			want: "# TYPE http_request_duration_seconds histogram\n" +
				"http_request_duration_seconds_bucket{le=\"0.05\"} 24054\n" +
				"http_request_duration_seconds_bucket{le=\"0.1\"} 33444\n" +
				"http_request_duration_seconds_bucket{le=\"0.2\"} 100392\n" +
				"http_request_duration_seconds_bucket{le=\"0.5\"} 129389\n" +
				"http_request_duration_seconds_bucket{le=\"1\"} 133988\n" +
				"http_request_duration_seconds_bucket{le=\"+Inf\"} 144320\n" +
				"http_request_duration_seconds_sum 53423\n" +
				"http_request_duration_seconds_count 144320\n",
		},
		{
			//: le goes LAST, after the series' own labels, and every member of
			//: the family repeats those labels.
			name: "a labelled histogram carries le last",
			snap: coremetrics.SnapshotValue{Histograms: map[string][]coremetrics.HistogramValue{
				"latency": {{
					Labels:  labelsOf("route", "/v1"),
					Buckets: []float64{1},
					Counts:  []uint64{3, 1},
					Sum:     4.5,
					Count:   4,
				}},
			}},
			want: "# TYPE latency histogram\n" +
				"latency_bucket{route=\"/v1\",le=\"1\"} 3\n" +
				"latency_bucket{route=\"/v1\",le=\"+Inf\"} 4\n" +
				"latency_sum{route=\"/v1\"} 4.5\n" +
				"latency_count{route=\"/v1\"} 4\n",
		},
		{
			//: a histogram with no declared bounds still owes the mandatory
			//: +Inf line — a family without it is not a histogram.
			name: "a bucketless histogram still emits +Inf",
			snap: coremetrics.SnapshotValue{Histograms: map[string][]coremetrics.HistogramValue{
				"latency": {{Counts: []uint64{5}, Sum: 10, Count: 5}},
			}},
			want: "# TYPE latency histogram\n" +
				"latency_bucket{le=\"+Inf\"} 5\n" +
				"latency_sum 10\n" +
				"latency_count 5\n",
		},
		{
			//: a +Inf BOUND would forge a second le="+Inf" line, i.e. a
			//: duplicate series; it is skipped and its count still lands in
			//: the mandatory one.
			name: "a non-finite declared bound folds into +Inf instead of duplicating it",
			snap: coremetrics.SnapshotValue{Histograms: map[string][]coremetrics.HistogramValue{
				"latency": {{
					Buckets: []float64{math.NaN(), 1, math.Inf(1)},
					Counts:  []uint64{2, 3, 4, 5},
					Sum:     1,
					Count:   14,
				}},
			}},
			want: "# TYPE latency histogram\n" +
				"latency_bucket{le=\"1\"} 5\n" +
				"latency_bucket{le=\"+Inf\"} 14\n" +
				"latency_sum 1\n" +
				"latency_count 14\n",
		},
		{
			//: the three kinds appear in a fixed order, so the whole document
			//: is stable and not merely each section.
			name: "every kind, in a fixed order",
			snap: coremetrics.SnapshotValue{
				Counters:   map[string][]coremetrics.CounterValue{"c": {{Value: 1}}},
				Gauges:     map[string][]coremetrics.GaugeValue{"g": {{Value: 2}}},
				Histograms: map[string][]coremetrics.HistogramValue{"h": {{Count: 3}}},
			},
			want: "# TYPE c counter\nc 1\n" +
				"# TYPE g gauge\ng 2\n" +
				"# TYPE h histogram\nh_bucket{le=\"+Inf\"} 0\nh_sum 0\nh_count 3\n",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := exportPrometheus(t, c.snap)
		if got != c.want {
			t.Errorf("Export wrote\n%q\nwant\n%q", got, c.want)
		}
		//: two exports of one snapshot must be byte-identical, or the output
		//: is not diffable and Go's randomised map order leaks into it.
		if second := exportPrometheus(t, c.snap); second != got {
			t.Errorf("two exports of one snapshot differ:\n%q\n%q", got, second)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestPrometheusExporterHeaderAppearsOncePerName pins the invariant the format
// states outright — "only one HELP and one TYPE line are permitted per metric
// name" — across a family with many series, where a per-series header would be
// the natural bug.
func TestPrometheusExporterHeaderAppearsOncePerName(t *testing.T) {
	t.Parallel()
	series := make([]coremetrics.CounterValue, 0, 64)
	for i := range 64 {
		series = append(series, coremetrics.CounterValue{
			Labels: labelsOf("shard", string(rune('a'+i%26))),
			Value:  int64(i),
		})
	}
	got := exportPrometheus(t, coremetrics.SnapshotValue{
		Counters: map[string][]coremetrics.CounterValue{"requests_total": series},
	})

	if n := strings.Count(got, "# TYPE requests_total counter\n"); n != 1 {
		t.Errorf("the TYPE header appears %d times, want exactly 1", n)
	}
	//: and it must precede every sample of that family, which is the other
	//: half of the format's rule.
	if !strings.HasPrefix(got, "# TYPE requests_total counter\n") {
		t.Errorf("the TYPE header does not precede its samples:\n%q", got[:min(len(got), 120)])
	}
}

// TestPrometheusExporterEscapesAdversarialLabelValues is the security case.
//
// A label value is data — a route, a tenant id, a header a caller does not
// control. Unescaped, a value holding a quote or a newline forges a line that a
// reader parses as ANOTHER series, which turns a metrics dump into an injection
// surface. Each case below is a value engineered to do exactly that, and the
// assertion is not only on the bytes but on the LINE COUNT: a forged line would
// show up there even if the golden string were updated to match a bug.
func TestPrometheusExporterEscapesAdversarialLabelValues(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		value string
		want  string
	}
	tests := []tc{
		{
			name:  "a newline that would forge a whole extra series",
			value: "x\nevil_metric 999",
			want:  `requests_total{route="x\nevil_metric 999"} 1`,
		},
		{
			name:  "a quote that would close the value early",
			value: `a"b`,
			want:  `requests_total{route="a\"b"} 1`,
		},
		{
			//: the classic escape-the-escape: a trailing backslash would
			//: otherwise consume the closing quote.
			name:  "a trailing backslash that would eat the closing quote",
			value: `C:\`,
			want:  `requests_total{route="C:\\"} 1`,
		},
		{
			name:  "a value that closes the brace and forges a second line",
			value: "\"} 0\nforged_total{x=\"",
			want:  `requests_total{route="\"} 0\nforged_total{x=\""} 1`,
		},
		{
			//: the published escaping example from the format documentation.
			name:  "the documentation's own escaping example",
			value: "Cannot find file:\n\"FILE.TXT\"",
			want:  `requests_total{route="Cannot find file:\n\"FILE.TXT\""} 1`,
		},
		{
			//: a carriage return is NOT escaped, on purpose. The format
			//: defines exactly three escape sequences; \r is not one of them,
			//: so emitting "\r" would make the document unparseable ("invalid
			//: escape sequence"). It cannot forge a line either — only \n
			//: terminates one.
			name:  "a carriage return passes through verbatim",
			value: "a\rb",
			want:  "requests_total{route=\"a\rb\"} 1",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := exportPrometheus(t, coremetrics.SnapshotValue{
			Counters: map[string][]coremetrics.CounterValue{
				"requests_total": {{Labels: labelsOf("route", c.value), Value: 1}},
			},
		})
		want := "# TYPE requests_total counter\n" + c.want + "\n"
		if got != want {
			t.Errorf("Export wrote\n%q\nwant\n%q", got, want)
		}
		//: the structural assertion: header + one sample + the trailing
		//: newline's empty tail. A forged line lands here first.
		if lines := strings.Split(got, "\n"); len(lines) != 3 {
			t.Errorf("the document has %d newline-separated parts, want 3: %q", len(lines), lines)
		}
		//: and no backslash may ever be followed by anything but the three
		//: sequences the 0.0.4 parser knows, or the document is rejected.
		assertOnlyLegalEscapes(t, got)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// assertOnlyLegalEscapes fails when doc contains a backslash followed by
// anything other than the three sequences the exposition format defines.
//
// This is the escaping contract stated from the READER's side: inventing a
// fourth escape (say \r or \t) would be worse than not escaping at all, because
// the reference parser errors on an unknown escape sequence and drops the whole
// scrape rather than one label.
func assertOnlyLegalEscapes(t *testing.T, doc string) {
	t.Helper()
	//: the stride is not uniform — a legal escape consumes TWO bytes, or
	//: `\\"` would be re-read as `\"` and pass a document that is broken.
	for i := 0; i < len(doc); {
		if doc[i] != '\\' {
			i++
			continue
		}
		if i+1 >= len(doc) {
			t.Errorf("document ends on a dangling backslash: %q", doc)
			return
		}
		switch doc[i+1] {
		//: the three sequences the exposition format defines.
		case '\\', '"', 'n':
			i += 2
		//: a fourth one would make the reference parser drop the scrape.
		default:
			t.Errorf("illegal escape sequence %q in %q", doc[i:i+2], doc)
			i += 2
		}
	}
}

// TestPrometheusExporterRefusesUnrepresentableNames pins the conformance
// decision: a name the format cannot spell REFUSES the export, typed, and
// nothing is written.
//
// Transliterating (mapping the offending bytes to "_") was rejected because it
// is not injective: `a.b`, `a-b` and `a b` all become `a_b`, so two distinct
// instruments silently merge into one family and no consumer can tell. That is
// the same forge-by-collision hazard the length-prefixed series key exists to
// prevent, and it can even merge two different instrument KINDS under one name.
// Skipping the offender was rejected as the inert behaviour ADR 0031 bans.
//
// Refusing is safe here for the reason the meter already panics on a bad label
// KEY: an instrument name is STRUCTURE. It is a literal at the call site and
// constant for the process, so it is wrong on the first scrape or never — it
// cannot start failing in production because of traffic.
func TestPrometheusExporterRefusesUnrepresentableNames(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		snap coremetrics.SnapshotValue
		want kerrs.Code
	}
	counter := func(name string, labels ...string) coremetrics.SnapshotValue {
		return coremetrics.SnapshotValue{Counters: map[string][]coremetrics.CounterValue{
			name: {{Labels: labelsOf(labels...), Value: 1}},
		}}
	}
	tests := []tc{
		{"a dotted metric name", counter("http.requests"), svcmetrics.CodeInvalidMetricName},
		{"a dashed metric name", counter("http-requests"), svcmetrics.CodeInvalidMetricName},
		{"a metric name opening on a digit", counter("5xx"), svcmetrics.CodeInvalidMetricName},
		{"an empty metric name", counter(""), svcmetrics.CodeInvalidMetricName},
		{"a metric name holding a quote", counter(`a"b`), svcmetrics.CodeInvalidMetricName},
		{"a metric name holding a newline", counter("a\nb 9"), svcmetrics.CodeInvalidMetricName},
		{"a UTF-8 metric name", counter("µs_total"), svcmetrics.CodeInvalidMetricName},
		{"a dotted label name", counter("ok", "http.method", "GET"), svcmetrics.CodeInvalidLabelName},
		{"a colon in a label name", counter("ok", "a:b", "GET"), svcmetrics.CodeInvalidLabelName},
		{"a label name opening on a digit", counter("ok", "5xx", "GET"), svcmetrics.CodeInvalidLabelName},
		{"a label name holding a quote", counter("ok", `a"b`, "GET"), svcmetrics.CodeInvalidLabelName},
		{"a server-reserved label prefix", counter("ok", "__name__", "x"), svcmetrics.CodeReservedLabelName},
		{
			"le on a histogram, which owns it",
			coremetrics.SnapshotValue{Histograms: map[string][]coremetrics.HistogramValue{
				"latency": {{Labels: labelsOf("le", "1"), Count: 1}},
			}},
			svcmetrics.CodeReservedLabelName,
		},
		{
			"a gauge name the format cannot spell",
			coremetrics.SnapshotValue{Gauges: map[string][]coremetrics.GaugeValue{
				"in flight": {{Value: 1}},
			}},
			svcmetrics.CodeInvalidMetricName,
		},
		{
			"a histogram name the format cannot spell",
			coremetrics.SnapshotValue{Histograms: map[string][]coremetrics.HistogramValue{
				"latency/ms": {{Count: 1}},
			}},
			svcmetrics.CodeInvalidMetricName,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var buf bytes.Buffer
		err := svcmetrics.NewPrometheusExporter("test", &buf).Export(c.snap)

		if !kerrs.HasCode(err, c.want) {
			t.Fatalf("Export = %v, want code %v", err, c.want)
		}
		//: the refusal is atomic: a rejected document is not half-written,
		//: because a truncated exposition parses as a complete one.
		if buf.Len() != 0 {
			t.Errorf("Export wrote %q on a refusal, want nothing", buf.String())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestPrometheusExporterRefusalIsAtomicAcrossFamilies pins that a bad name
// found in the LAST family still suppresses the good ones rendered before it.
func TestPrometheusExporterRefusalIsAtomicAcrossFamilies(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	err := svcmetrics.NewPrometheusExporter("test", &buf).Export(coremetrics.SnapshotValue{
		Counters:   map[string][]coremetrics.CounterValue{"good_total": {{Value: 1}}},
		Histograms: map[string][]coremetrics.HistogramValue{"bad.name": {{Count: 1}}},
	})

	if !kerrs.HasCode(err, svcmetrics.CodeInvalidMetricName) {
		t.Fatalf("Export = %v, want INVALID_METRIC_NAME", err)
	}
	if buf.Len() != 0 {
		t.Errorf("Export wrote %q, want nothing at all", buf.String())
	}
}

// TestPrometheusExporterEmitsTheOverflowSeries pins that the meter's aggregated
// overflow series reaches the wire as an ORDINARY series.
//
// It has to: the whole point of folding rather than dropping is that an
// operator can see the fold and alert on it. A consumer alerts with
// `{sdk_metric_overflow="true"}`, which only works if the exporter emits it
// like any other label — hiding it would restore the silent failure the
// cardinality policy exists to avoid.
func TestPrometheusExporterEmitsTheOverflowSeries(t *testing.T) {
	t.Parallel()
	meter := svcmetrics.NewMeterWithConfig(svcmetrics.MeterConfig{MaxSeriesPerInstrument: 1})
	meter.Counter("requests_total", coremetrics.LabelValue{Key: "route", Value: "/a"}).Inc()
	//: past the bound of one, every further label set folds into overflow.
	meter.Counter("requests_total", coremetrics.LabelValue{Key: "route", Value: "/b"}).Inc()
	meter.Counter("requests_total", coremetrics.LabelValue{Key: "route", Value: "/c"}).Inc()

	got := exportPrometheus(t, meter.Collect())

	//: the reserved key is spelled with underscores precisely so it needs no
	//: mangling to be a legal Prometheus label name.
	want := "# TYPE requests_total counter\n" +
		"requests_total{route=\"/a\"} 1\n" +
		"requests_total{" + coremetrics.OverflowLabelKey + "=\"" + coremetrics.OverflowLabelValue + "\"} 2\n"
	if got != want {
		t.Errorf("Export wrote\n%q\nwant\n%q", got, want)
	}
}

// TestPrometheusExporterFromARealMeter walks the whole path a consumer walks:
// mint instruments, observe, Collect, Export.
func TestPrometheusExporterFromARealMeter(t *testing.T) {
	t.Parallel()
	meter := svcmetrics.NewMeter()
	meter.Counter("requests_total", coremetrics.LabelValue{Key: "method", Value: "GET"}).Add(3)
	meter.Gauge("in_flight").Set(2.5)
	hist := meter.Histogram("latency_seconds", []float64{0.5, 1})
	hist.Record(0.25)
	hist.Record(0.75)
	hist.Record(4)

	got := exportPrometheus(t, meter.Collect())

	want := "# TYPE requests_total counter\n" +
		"requests_total{method=\"GET\"} 3\n" +
		"# TYPE in_flight gauge\n" +
		"in_flight 2.5\n" +
		"# TYPE latency_seconds histogram\n" +
		"latency_seconds_bucket{le=\"0.5\"} 1\n" +
		"latency_seconds_bucket{le=\"1\"} 2\n" +
		"latency_seconds_bucket{le=\"+Inf\"} 3\n" +
		"latency_seconds_sum 5\n" +
		"latency_seconds_count 3\n"
	if got != want {
		t.Errorf("Export wrote\n%q\nwant\n%q", got, want)
	}
}

// TestPrometheusExporterWriterFailure pins that a dead sink is reported typed.
func TestPrometheusExporterWriterFailure(t *testing.T) {
	t.Parallel()
	sinkErr := errors.New("the sink is gone")

	err := svcmetrics.NewPrometheusExporter("test", failingWriter{err: sinkErr}).
		Export(coremetrics.SnapshotValue{
			Counters: map[string][]coremetrics.CounterValue{"c": {{Value: 1}}},
		})

	if !kerrs.HasCode(err, coremetrics.CodeExportFailed) {
		t.Fatalf("Export = %v, want EXPORT_FAILED", err)
	}
	//: the sink's own error stays reachable, or an operator cannot tell a
	//: closed pipe from a full disk.
	if !errors.Is(err, sinkErr) {
		t.Errorf("Export = %v, want it to wrap %v", err, sinkErr)
	}
}

// TestPrometheusExporterRegistry pins that importing this package registers the
// exporter, which is the only way a consumer reaches it by name.
func TestPrometheusExporterRegistry(t *testing.T) {
	t.Parallel()
	got, ok := coremetrics.LookupExporter("prometheus")
	if !ok {
		t.Fatalf("%q did not self-register on import", "prometheus")
	}
	if got.Name() != "prometheus" {
		t.Errorf("the exporter under %q reports name %q", "prometheus", got.Name())
	}
	//: and it must be listed, since that is what an operator reads.
	var listed bool
	for _, name := range coremetrics.AvailableExporters() {
		if name == "prometheus" {
			listed = true
		}
	}
	if !listed {
		t.Errorf("%q is registered but not listed", "prometheus")
	}
	//: exporting through the registry reaches the same implementation.
	if err := coremetrics.Export("prometheus", coremetrics.SnapshotValue{}); err != nil {
		t.Errorf("Export through the registry = %v, want nil", err)
	}
}
