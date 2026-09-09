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

// labelsOf is the string-attribute shorthand the tables below share. Every
// case here is a STRING attribute, because that is the only kind the Prometheus
// wire can carry without losing its type — the typed kinds get their own test.
func labelsOf(pairs ...string) []coremetrics.AttrValue {
	out := make([]coremetrics.AttrValue, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, coremetrics.String(pairs[i], pairs[i+1]))
	}
	return out
}

// promCounter wraps points as the CUMULATIVE, MONOTONIC sum metric a snapshot
// carries for a Counter. Temporality is spelled out in every case because it is
// the one field the Prometheus wire has no room for, and a delta metric is
// refused rather than mis-labelled.
func promCounter(points ...coremetrics.SumValue) coremetrics.SumMetricValue {
	return coremetrics.SumMetricValue{
		Temporality: coremetrics.TemporalityCumulative,
		Monotonic:   true,
		Points:      points,
	}
}

// promUpDown wraps points as the cumulative NON-MONOTONIC sum an
// UpDownCounter produces — the metric Prometheus must type `gauge`.
func promUpDown(points ...coremetrics.SumValue) coremetrics.SumMetricValue {
	return coremetrics.SumMetricValue{
		Temporality: coremetrics.TemporalityCumulative,
		Points:      points,
	}
}

// promGauge wraps points as a gauge metric, which carries no temporality at all.
func promGauge(points ...coremetrics.GaugeValue) coremetrics.GaugeMetricValue {
	return coremetrics.GaugeMetricValue{Points: points}
}

// promHistogram wraps points as a cumulative histogram metric.
func promHistogram(points ...coremetrics.HistogramValue) coremetrics.HistogramMetricValue {
	return coremetrics.HistogramMetricValue{
		Temporality: coremetrics.TemporalityCumulative,
		Points:      points,
	}
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
			snap: coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
				"requests_total": promCounter(coremetrics.SumValue{Value: 7}),
			}},
			want: "# TYPE requests_total counter\nrequests_total 7\n",
		},
		{
			//: the header is written ONCE, then every series under it — the
			//: property the name-keyed snapshot shape exists to make free.
			name: "one header, several series",
			snap: coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
				"requests_total": promCounter(coremetrics.SumValue{Attrs: labelsOf("method", "GET"), Value: 7}, coremetrics.SumValue{Attrs: labelsOf("method", "POST"), Value: 2}),
			}},
			want: "# TYPE requests_total counter\n" +
				"requests_total{method=\"GET\"} 7\n" +
				"requests_total{method=\"POST\"} 2\n",
		},
		{
			name: "several labels stay in the snapshot's canonical order",
			snap: coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
				"requests_total": promCounter(coremetrics.SumValue{Attrs: labelsOf("code", "200", "method", "post"), Value: 1027}),
			}},
			want: "# TYPE requests_total counter\n" +
				"requests_total{code=\"200\",method=\"post\"} 1027\n",
		},
		{
			//: declared out of order; the document must be sorted by name.
			name: "families are sorted by name",
			snap: coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
				"z": promCounter(coremetrics.SumValue{Value: 1}), "a": promCounter(coremetrics.SumValue{Value: 2}),
			}},
			want: "# TYPE a counter\na 2\n# TYPE z counter\nz 1\n",
		},
		{
			//: a colon is legal in a metric name — it is what recording rules
			//: use — and illegal in a label name, which the refusal table pins.
			name: "a colon is legal in a metric name",
			snap: coremetrics.SnapshotValue{Gauges: map[string]coremetrics.GaugeMetricValue{
				"job:rate:5m": promGauge(coremetrics.GaugeValue{Value: 2.5}),
			}},
			want: "# TYPE job:rate:5m gauge\njob:rate:5m 2.5\n",
		},
		{
			//: the format names NaN / +Inf / -Inf as valid values, and Go's
			//: own FormatFloat spells all three exactly that way.
			name: "a gauge spells the non-finite values the format names",
			snap: coremetrics.SnapshotValue{Gauges: map[string]coremetrics.GaugeMetricValue{
				"a_nan": promGauge(coremetrics.GaugeValue{Value: math.NaN()}),
				"b_pos": promGauge(coremetrics.GaugeValue{Value: math.Inf(1)}),
				"c_neg": promGauge(coremetrics.GaugeValue{Value: math.Inf(-1)}),
			}},
			want: "# TYPE a_nan gauge\na_nan NaN\n" +
				"# TYPE b_pos gauge\nb_pos +Inf\n" +
				"# TYPE c_neg gauge\nc_neg -Inf\n",
		},
		{
			//: the published example from the exposition-format documentation,
			//: with the per-bucket counts the meter stores.
			name: "the documentation's own histogram family",
			snap: coremetrics.SnapshotValue{Histograms: map[string]coremetrics.HistogramMetricValue{
				"http_request_duration_seconds": promHistogram(coremetrics.HistogramValue{
					Bounds: []float64{0.05, 0.1, 0.2, 0.5, 1},
					Counts: []uint64{24054, 9390, 66948, 28997, 4599, 10332},
					Sum:    53423,
					Count:  144320,
				}),
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
			snap: coremetrics.SnapshotValue{Histograms: map[string]coremetrics.HistogramMetricValue{
				"latency": promHistogram(coremetrics.HistogramValue{
					Attrs:  labelsOf("route", "/v1"),
					Bounds: []float64{1},
					Counts: []uint64{3, 1},
					Sum:    4.5,
					Count:  4,
				}),
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
			snap: coremetrics.SnapshotValue{Histograms: map[string]coremetrics.HistogramMetricValue{
				"latency": promHistogram(coremetrics.HistogramValue{Counts: []uint64{5}, Sum: 10, Count: 5}),
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
			snap: coremetrics.SnapshotValue{Histograms: map[string]coremetrics.HistogramMetricValue{
				"latency": promHistogram(coremetrics.HistogramValue{
					Bounds: []float64{math.NaN(), 1, math.Inf(1)},
					Counts: []uint64{2, 3, 4, 5},
					Sum:    1,
					Count:  14,
				}),
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
				Sums:       map[string]coremetrics.SumMetricValue{"c": promCounter(coremetrics.SumValue{Value: 1})},
				Gauges:     map[string]coremetrics.GaugeMetricValue{"g": promGauge(coremetrics.GaugeValue{Value: 2})},
				Histograms: map[string]coremetrics.HistogramMetricValue{"h": promHistogram(coremetrics.HistogramValue{Count: 3})},
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
	series := make([]coremetrics.SumValue, 0, 64)
	for i := range 64 {
		series = append(series, coremetrics.SumValue{
			Attrs: labelsOf("shard", string(rune('a'+i%26))),
			Value: int64(i),
		})
	}
	got := exportPrometheus(t, coremetrics.SnapshotValue{
		Sums: map[string]coremetrics.SumMetricValue{"requests_total": promCounter(series...)},
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
			Sums: map[string]coremetrics.SumMetricValue{
				"requests_total": promCounter(coremetrics.SumValue{Attrs: labelsOf("route", c.value), Value: 1}),
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
		return coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
			name: promCounter(coremetrics.SumValue{Attrs: labelsOf(labels...), Value: 1}),
		}}
	}
	//: the attribute keys the OTel semantic conventions actually specify are
	//: DOTTED, so adopting the OTel model means the idiomatic key is the one
	//: this wire refuses. Naming it here keeps the loss executable.
	otelKey := counter("ok", "http.request.method", "GET")
	tests := []tc{
		{"a dotted metric name", counter("http.requests"), svcmetrics.CodeInvalidMetricName},
		{"a dashed metric name", counter("http-requests"), svcmetrics.CodeInvalidMetricName},
		{"a metric name opening on a digit", counter("5xx"), svcmetrics.CodeInvalidMetricName},
		{"an empty metric name", counter(""), svcmetrics.CodeInvalidMetricName},
		{"a metric name holding a quote", counter(`a"b`), svcmetrics.CodeInvalidMetricName},
		{"a metric name holding a newline", counter("a\nb 9"), svcmetrics.CodeInvalidMetricName},
		{"a UTF-8 metric name", counter("µs_total"), svcmetrics.CodeInvalidMetricName},
		{"a dotted label name", counter("ok", "http.method", "GET"), svcmetrics.CodeInvalidLabelName},
		{"an OTel-conventional attribute key", otelKey, svcmetrics.CodeInvalidLabelName},
		{"a colon in a label name", counter("ok", "a:b", "GET"), svcmetrics.CodeInvalidLabelName},
		{"a label name opening on a digit", counter("ok", "5xx", "GET"), svcmetrics.CodeInvalidLabelName},
		{"a label name holding a quote", counter("ok", `a"b`, "GET"), svcmetrics.CodeInvalidLabelName},
		{"a server-reserved label prefix", counter("ok", "__name__", "x"), svcmetrics.CodeReservedLabelName},
		{
			"le on a histogram, which owns it",
			coremetrics.SnapshotValue{Histograms: map[string]coremetrics.HistogramMetricValue{
				"latency": promHistogram(coremetrics.HistogramValue{Attrs: labelsOf("le", "1"), Count: 1}),
			}},
			svcmetrics.CodeReservedLabelName,
		},
		{
			"a gauge name the format cannot spell",
			coremetrics.SnapshotValue{Gauges: map[string]coremetrics.GaugeMetricValue{
				"in flight": promGauge(coremetrics.GaugeValue{Value: 1}),
			}},
			svcmetrics.CodeInvalidMetricName,
		},
		{
			"a histogram name the format cannot spell",
			coremetrics.SnapshotValue{Histograms: map[string]coremetrics.HistogramMetricValue{
				"latency/ms": promHistogram(coremetrics.HistogramValue{Count: 1}),
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
		Sums:       map[string]coremetrics.SumMetricValue{"good_total": promCounter(coremetrics.SumValue{Value: 1})},
		Histograms: map[string]coremetrics.HistogramMetricValue{"bad.name": promHistogram(coremetrics.HistogramValue{Count: 1})},
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
	meter.Counter("requests_total", coremetrics.String("route", "/a")).Inc()
	//: past the bound of one, every further label set folds into overflow.
	meter.Counter("requests_total", coremetrics.String("route", "/b")).Inc()
	meter.Counter("requests_total", coremetrics.String("route", "/c")).Inc()

	got := exportPrometheus(t, meter.Collect())

	//: the reserved key is spelled with underscores precisely so it needs no
	//: mangling to be a legal Prometheus label name.
	//: the marker is a BOOL attribute now, and it still reaches the wire as
	//: the same "true" — a Prometheus label value is a string, so the type is
	//: exactly what this connector loses.
	want := "# TYPE requests_total counter\n" +
		"requests_total{route=\"/a\"} 1\n" +
		"requests_total{" + coremetrics.OverflowAttrKey + "=\"true\"} 2\n"
	if got != want {
		t.Errorf("Export wrote\n%q\nwant\n%q", got, want)
	}
}

// TestPrometheusExporterFromARealMeter walks the whole path a consumer walks:
// mint instruments, observe, Collect, Export.
func TestPrometheusExporterFromARealMeter(t *testing.T) {
	t.Parallel()
	meter := svcmetrics.NewMeter()
	meter.Counter("requests_total", coremetrics.String("method", "GET")).Add(3)
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
			Sums: map[string]coremetrics.SumMetricValue{"c": promCounter(coremetrics.SumValue{Value: 1})},
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

// TestPrometheusTypesANonMonotonicSumAsAGauge pins the mapping that keeps
// `rate()` honest.
//
// A Counter and an UpDownCounter produce the SAME point shape in the OTel data
// model and differ only by SumMetricValue.Monotonic. Prometheus has no such field —
// it has two TYPE words — so the mapping has to happen here. Typing a
// non-monotonic sum as `counter` would make the server treat every decrease as
// a process restart and re-extrapolate from zero, inventing a spike on a
// metric that merely went down.
func TestPrometheusTypesANonMonotonicSumAsAGauge(t *testing.T) {
	t.Parallel()
	got := exportPrometheus(t, coremetrics.SnapshotValue{
		Sums: map[string]coremetrics.SumMetricValue{
			"in_flight":      promUpDown(coremetrics.SumValue{Value: 2}),
			"requests_total": promCounter(coremetrics.SumValue{Value: 7}),
		},
	})
	want := "# TYPE in_flight gauge\nin_flight 2\n" +
		"# TYPE requests_total counter\nrequests_total 7\n"
	if got != want {
		t.Errorf("Export wrote\n%q\nwant\n%q", got, want)
	}
}

// TestPrometheusRefusesADeltaSnapshot pins the loudest of this connector's
// losses.
//
// The exposition format has no temporality field and a Prometheus server reads
// every counter as cumulative: `rate()` differences successive scrapes itself.
// Handing it delta values means it differences numbers that are already
// differences, and a window smaller than the last one reads as a counter reset.
// Nothing in the document would say so and no dashboard would look broken.
//
// Refusing is safe for the same reason refusing a name is: a meter's
// temporality is fixed at construction, so this fails on the first scrape or
// never — it cannot start failing under traffic.
func TestPrometheusRefusesADeltaSnapshot(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		snap coremetrics.SnapshotValue
	}
	delta := func(m coremetrics.SumMetricValue) coremetrics.SumMetricValue {
		m.Temporality = coremetrics.TemporalityDelta
		return m
	}
	tests := []tc{
		{
			name: "a delta counter",
			snap: coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
				"requests_total": delta(promCounter(coremetrics.SumValue{Value: 7})),
			}},
		},
		{
			name: "a delta up-down counter",
			snap: coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
				"in_flight": delta(promUpDown(coremetrics.SumValue{Value: 2})),
			}},
		},
		{
			name: "a delta histogram",
			snap: coremetrics.SnapshotValue{Histograms: map[string]coremetrics.HistogramMetricValue{
				"latency": {Temporality: coremetrics.TemporalityDelta, Points: []coremetrics.HistogramValue{{Count: 1}}},
			}},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var buf bytes.Buffer
		err := svcmetrics.NewPrometheusExporter("test", &buf).Export(c.snap)
		if !kerrs.HasCode(err, svcmetrics.CodeUnsupportedTemporality) {
			t.Fatalf("Export = %v, want UNSUPPORTED_TEMPORALITY", err)
		}
		//: nothing is written, exactly as for a refused name.
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

// TestPrometheusFlattensEveryAttributeKindToAString is the type loss, made
// executable.
//
// A Prometheus label value IS a string; there is no second option and no
// encoding avoids it. So Int64("v", 1) and String("v", "1") — two distinct
// series in the snapshot, kept apart by the kind tag in the series key —
// become ONE series on this wire. That is not a bug to fix here, it is the
// property that makes this exporter a connector rather than a rendering, and
// the assertion below is what stops it from being discovered by surprise.
func TestPrometheusFlattensEveryAttributeKindToAString(t *testing.T) {
	t.Parallel()
	got := exportPrometheus(t, coremetrics.SnapshotValue{
		Sums: map[string]coremetrics.SumMetricValue{
			"requests_total": promCounter(
				coremetrics.SumValue{Attrs: []coremetrics.AttrValue{coremetrics.Bool("cached", true)}, Value: 1},
				coremetrics.SumValue{Attrs: []coremetrics.AttrValue{coremetrics.Float64("ratio", 0.5)}, Value: 2},
				coremetrics.SumValue{Attrs: []coremetrics.AttrValue{coremetrics.Int64("status", 503)}, Value: 3},
			),
		},
	})
	want := "# TYPE requests_total counter\n" +
		"requests_total{cached=\"true\"} 1\n" +
		"requests_total{ratio=\"0.5\"} 2\n" +
		"requests_total{status=\"503\"} 3\n"
	if got != want {
		t.Errorf("Export wrote\n%q\nwant\n%q", got, want)
	}

	//: and the collapse itself: two series the snapshot keeps apart render as
	//: two IDENTICAL label sets, which a Prometheus server merges.
	collapsed := exportPrometheus(t, coremetrics.SnapshotValue{
		Sums: map[string]coremetrics.SumMetricValue{
			"requests_total": promCounter(
				coremetrics.SumValue{Attrs: []coremetrics.AttrValue{coremetrics.Int64("v", 1)}, Value: 1},
				coremetrics.SumValue{Attrs: []coremetrics.AttrValue{coremetrics.String("v", "1")}, Value: 2},
			),
		},
	})
	if strings.Count(collapsed, "requests_total{v=\"1\"}") != 2 {
		t.Errorf("the two kinds did not both render as v=\"1\":\n%q", collapsed)
	}
}

// TestPrometheusDropsResourceAndScope pins the second loss: neither identity
// reaches the wire, and in particular service.name CANNOT — a Prometheus label
// name is [a-zA-Z_][a-zA-Z0-9_]* and the dot is outside it.
//
// The OTel-to-Prometheus interoperability specification answers this by
// mangling the key into a `target_info` metric. This SDK does not, because the
// mangling is not injective and this repo refuses non-injective name rewriting
// everywhere else in the same file.
func TestPrometheusDropsResourceAndScope(t *testing.T) {
	t.Parallel()
	meter := svcmetrics.NewMeterWithConfig(svcmetrics.MeterConfig{
		Resource: coremetrics.ResourceValue{Attrs: []coremetrics.AttrValue{
			coremetrics.String(coremetrics.ServiceNameKey, "orders"),
		}},
		Scope: coremetrics.ScopeValue{Name: "github.com/acme/orders", Version: "1.4.0"},
	})
	meter.Counter("requests_total").Inc()

	got := exportPrometheus(t, meter.Collect())

	if got != "# TYPE requests_total counter\nrequests_total 1\n" {
		t.Errorf("Export wrote\n%q\nwant only the counter family", got)
	}
	for _, dropped := range []string{"orders", "service", "target_info", "1.4.0"} {
		if strings.Contains(got, dropped) {
			t.Errorf("the document leaked %q, which this connector does not carry:\n%q", dropped, got)
		}
	}
}
