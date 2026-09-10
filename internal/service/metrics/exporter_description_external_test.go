// Package metrics_test — what each of the three exporters does with an
// instrument description: present, absent, and adversarial (ADR 0067).
package metrics_test

import (
	"bytes"
	"strings"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	svcmetrics "github.com/kitsunium/sdk/internal/service/metrics"
)

// describedSnapshot is one metric of each kind, each carrying help.
func describedSnapshot(help string) coremetrics.SnapshotValue {
	return coremetrics.SnapshotValue{
		Sums: map[string]coremetrics.SumMetricValue{
			"requests_total": {
				Temporality: coremetrics.TemporalityCumulative,
				Monotonic:   true,
				Description: help,
				Points:      []coremetrics.SumValue{{Value: 1}},
			},
		},
		Gauges: map[string]coremetrics.GaugeMetricValue{
			"queue_depth": {
				Description: help,
				Points:      []coremetrics.GaugeValue{{Value: 2}},
			},
		},
		Histograms: map[string]coremetrics.HistogramMetricValue{
			"latency_seconds": {
				Temporality: coremetrics.TemporalityCumulative,
				Description: help,
				//: a ladder OTLP accepts: one count more than the bounds.
				Points: []coremetrics.HistogramValue{{
					Bounds: []float64{1},
					Counts: []uint64{2, 1},
					Sum:    4,
					Count:  3,
				}},
			},
		},
	}
}

// exportText renders snap through a fresh text exporter over a buffer.
func exportText(t *testing.T, snap coremetrics.SnapshotValue) string {
	t.Helper()
	var buf bytes.Buffer
	if err := svcmetrics.NewTextExporter("test", &buf).Export(snap); err != nil {
		t.Fatalf("Export = %v, want nil", err)
	}
	return buf.String()
}

// TestPrometheusHelpPrecedesTypeForEveryFamily pins the line the exporter's own
// comment promised for the day the Meter grew a description.
//
// The order is not decorative: the exposition format's published examples put
// HELP above TYPE, a parser reads both as metadata for the name that follows,
// and a HELP line emitted after the first sample would be metadata for a family
// that has already started.
func TestPrometheusHelpPrecedesTypeForEveryFamily(t *testing.T) {
	t.Parallel()
	got := exportPrometheus(t, describedSnapshot("Requests served, by route"))
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{
			name: "a counter family",
			want: "# HELP requests_total Requests served, by route\n" +
				"# TYPE requests_total counter\n",
		},
		{
			name: "a gauge family",
			want: "# HELP queue_depth Requests served, by route\n" +
				"# TYPE queue_depth gauge\n",
		},
		{
			name: "a histogram family",
			want: "# HELP latency_seconds Requests served, by route\n" +
				"# TYPE latency_seconds histogram\n",
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(got, c.want) {
				t.Errorf("the document does not carry\n%q\ngot:\n%s", c.want, got)
			}
		})
	}
}

// TestPrometheusHelpAppearsOncePerName pins the other half of the format's own
// rule — "only one HELP line may exist for any given metric name" — against the
// natural bug, which is emitting the header inside the per-series loop.
func TestPrometheusHelpAppearsOncePerName(t *testing.T) {
	t.Parallel()
	series := make([]coremetrics.SumValue, 0, 64)
	for i := range 64 {
		series = append(series, coremetrics.SumValue{
			Attrs: labelsOf("shard", string(rune('a'+i%26))),
			Value: int64(i),
		})
	}
	metric := promCounter(series...)
	metric.Description = "Requests served"
	got := exportPrometheus(t, coremetrics.SnapshotValue{
		Sums: map[string]coremetrics.SumMetricValue{"requests_total": metric},
	})

	if n := strings.Count(got, "# HELP requests_total"); n != 1 {
		t.Errorf("the HELP header appears %d times across 64 series, want exactly 1", n)
	}
	//: and it opens the family, ahead of TYPE and every sample.
	if !strings.HasPrefix(got, "# HELP requests_total Requests served\n# TYPE requests_total counter\n") {
		t.Errorf("HELP does not open the family:\n%q", got[:min(len(got), 160)])
	}
}

// TestPrometheusOmitsHelpWhenThereIsNoDescription pins the absent case.
//
// HELP is OPTIONAL in the exposition format, so an undescribed metric gets no
// line at all rather than `# HELP name ` with nothing after it. A blank
// docstring on every scrape is the placeholder this exporter refused to invent
// for as long as there was nothing real to print, and it would still be one.
func TestPrometheusOmitsHelpWhenThereIsNoDescription(t *testing.T) {
	t.Parallel()
	got := exportPrometheus(t, describedSnapshot(""))
	if strings.Contains(got, "# HELP") {
		t.Errorf("an undescribed snapshot emitted a HELP line:\n%s", got)
	}
	//: and the document is otherwise exactly what it was before descriptions
	//: existed — TYPE still opens every family.
	if !strings.HasPrefix(got, "# TYPE requests_total counter\n") {
		t.Errorf("the undescribed document changed shape:\n%q", got[:min(len(got), 120)])
	}
}

// TestPrometheusEscapesTheHelpDocstring is the security case for the docstring,
// and the one that distinguishes it from a label value.
//
// A description is prose the caller wrote, so it may contain anything. The
// format says a HELP docstring escapes exactly two characters — the backslash
// and the line feed — and NOT the double quote, because a docstring is the
// unquoted remainder of the line rather than a quoted token. Escaping the quote
// anyway would put a literal backslash into the help text an operator reads.
//
// The newline case is the dangerous one and is asserted on the LINE COUNT as
// well as on the bytes: unescaped, it would end the comment and let the rest of
// the description be parsed as a sample line — a forged series, the same hazard
// TestPrometheusExporterEscapesAdversarialLabelValues pins one line lower.
func TestPrometheusEscapesTheHelpDocstring(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		help  string
		want  string
		lines int
	}
	tests := []tc{
		{
			name:  "a newline that would forge a whole extra series",
			help:  "Requests\nevil_metric 999",
			want:  `# HELP requests_total Requests\nevil_metric 999`,
			lines: 2,
		},
		{
			name:  "a backslash that would eat the next character",
			help:  `A path like C:\temp`,
			want:  `# HELP requests_total A path like C:\\temp`,
			lines: 2,
		},
		{
			name:  "a double quote, which the format does NOT escape here",
			help:  `Requests to the "public" endpoint`,
			want:  `# HELP requests_total Requests to the "public" endpoint`,
			lines: 2,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		metric := promCounter(coremetrics.SumValue{Value: 1})
		metric.Description = c.help
		got := exportPrometheus(t, coremetrics.SnapshotValue{
			Sums: map[string]coremetrics.SumMetricValue{"requests_total": metric},
		})
		if !strings.Contains(got, c.want+"\n") {
			t.Errorf("HELP line = %q, want one carrying %q", got, c.want)
		}
		//: the document is HELP + TYPE + one sample; a forged line shows up
		//: here even if the golden string above were updated to match a bug.
		if n := strings.Count(got, "\n") - c.lines; n != 1 {
			t.Errorf("the document holds %d sample lines, want 1:\n%q", n, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestTextExporterRendersTheDescription pins the diagnostic exporter's answer.
//
// It renders it because it renders the whole model — that is what the file is
// for. An exporter that dropped the description would answer "did my Describe
// call reach the snapshot?" with silence, which is the one question a
// diagnostic exists to settle.
func TestTextExporterRendersTheDescription(t *testing.T) {
	t.Parallel()
	got := exportText(t, describedSnapshot("Requests served"))
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{
			name: "a sum keeps its qualifiers ahead of the description",
			want: `# metric requests_total sum cumulative monotonic description="Requests served"`,
		},
		{
			name: "a gauge has no qualifiers to keep",
			want: `# metric queue_depth gauge description="Requests served"`,
		},
		{
			name: "a histogram carries a temporality and no monotonicity",
			want: `# metric latency_seconds histogram cumulative description="Requests served"`,
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(got, c.want+"\n") {
				t.Errorf("the document does not carry\n%q\ngot:\n%s", c.want, got)
			}
		})
	}
}

// TestTextExporterOmitsAnAbsentDescription pins that the header stays exactly
// what it was, the way an absent scope version already does.
func TestTextExporterOmitsAnAbsentDescription(t *testing.T) {
	t.Parallel()
	got := exportText(t, describedSnapshot(""))
	if strings.Contains(got, "description=") {
		t.Errorf("an undescribed snapshot emitted a description key:\n%s", got)
	}
	if !strings.Contains(got, "# metric requests_total sum cumulative monotonic\n") {
		t.Errorf("the undescribed header changed shape:\n%s", got)
	}
}

// TestTextExporterEscapesTheDescription pins the OTHER escape.
//
// The text exporter QUOTES the description, so unlike the Prometheus docstring
// it must escape the double quote as well — the same three escapes it already
// applies to a string attribute value. The two exporters escaping differently
// is not an inconsistency: they are writing two different grammars, and each
// one follows the grammar it is writing.
func TestTextExporterEscapesTheDescription(t *testing.T) {
	t.Parallel()
	metric := promCounter(coremetrics.SumValue{Value: 1})
	metric.Description = "Requests to the \"public\" endpoint\nevil 9"
	got := exportText(t, coremetrics.SnapshotValue{
		Sums: map[string]coremetrics.SumMetricValue{"requests_total": metric},
	})
	want := `description="Requests to the \"public\" endpoint\nevil 9"`
	if !strings.Contains(got, want) {
		t.Errorf("the description is not escaped:\n%s", got)
	}
	//: three payload headers plus the header line plus one sample; a forged
	//: line would raise the count.
	if n := strings.Count(got, "\n"); n != 5 {
		t.Errorf("the document holds %d lines, want 5:\n%q", n, got)
	}
}

// TestOTLPJSONCarriesTheDescription pins the field, its spelling and its
// position — description is field 2, so it follows name and precedes the data
// oneof, which is the field-number order this encoder declares its structs in.
func TestOTLPJSONCarriesTheDescription(t *testing.T) {
	t.Parallel()
	doc, err := svcmetrics.EncodeOTLPJSON(describedSnapshot("Requests served"))
	if err != nil {
		t.Fatalf("EncodeOTLPJSON: unexpected error: %v", err)
	}
	got := string(doc)
	if !strings.Contains(got, `{"name":"requests_total","description":"Requests served","sum":{`) {
		t.Errorf("the sum metric does not carry description in field-number order:\n%s", got)
	}
	if !strings.Contains(got, `{"name":"queue_depth","description":"Requests served","gauge":{`) {
		t.Errorf("the gauge metric does not carry description:\n%s", got)
	}
	if !strings.Contains(got, `{"name":"latency_seconds","description":"Requests served","histogram":{`) {
		t.Errorf("the histogram metric does not carry description:\n%s", got)
	}
}

// TestOTLPJSONOmitsAnEmptyDescription pins the presence question, which is the
// one an OTLP encoder gets wrong silently.
//
// `description` is `string description = 2;` — a PLAIN proto3 string with no
// `optional`, so it has NO explicit presence and "" is indistinguishable from
// absent to a receiver. That puts it on the opposite side of the line from the
// three fields this encoder emits at their zero: asInt/asDouble are oneof
// members, a histogram point's sum is `optional double`, and isMonotonic is
// emitted because false is its surprising answer. An empty description has no
// surprising answer — it means nobody wrote one — so the key does not appear at
// all, which is also what the proto3-JSON default mapping does.
func TestOTLPJSONOmitsAnEmptyDescription(t *testing.T) {
	t.Parallel()
	doc, err := svcmetrics.EncodeOTLPJSON(describedSnapshot(""))
	if err != nil {
		t.Fatalf("EncodeOTLPJSON: unexpected error: %v", err)
	}
	got := string(doc)
	if strings.Contains(got, "description") {
		t.Errorf("an undescribed snapshot emitted the description field:\n%s", got)
	}
	//: and the metric is otherwise byte-identical to what it was before.
	if !strings.Contains(got, `{"name":"requests_total","sum":{`) {
		t.Errorf("the undescribed metric changed shape:\n%s", got)
	}
}
