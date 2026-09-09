// Package metrics — the Prometheus exporter's grammar + value-spelling units.
package metrics

import (
	"io"
	"math"
	"os"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// TestValidMetricName pins the metric-name grammar of the exposition format,
// [a-zA-Z_:][a-zA-Z0-9_:]*.
//
// The colon cases are the ones worth having: a colon is LEGAL in a metric name
// (Prometheus recording rules are named with it) and ILLEGAL in a label name,
// which is the single difference between the two grammars and the easiest thing
// to get wrong by sharing one validator.
func TestValidMetricName(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		input string
		want  bool
	}
	tests := []tc{
		{"a plain name", "requests_total", true},
		{"an underscore lead", "_internal", true},
		{"a colon lead", ":rule", true},
		{"a colon inside — a recording rule", "job:rate:5m", true},
		{"digits after the first byte", "http2_requests", true},
		{"upper case", "HTTPRequests", true},
		{"the reserved overflow key spells legally", "sdk_metric_overflow", true},
		{"empty", "", false},
		{"a digit lead", "5xx_total", false},
		{"a dot", "http.requests", false},
		{"a dash", "http-requests", false},
		{"a space", "http requests", false},
		{"a quote", `a"b`, false},
		{"a newline", "a\nb", false},
		{"a backslash", `a\b`, false},
		{"a brace", "a{b}", false},
		{"a multi-byte rune", "µs", false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := validMetricName(c.input); got != c.want {
				t.Errorf("validMetricName(%q) = %v, want %v", c.input, got, c.want)
			}
		})
	}
}

// TestValidLabelName pins the label-name grammar, [a-zA-Z_][a-zA-Z0-9_]* — the
// metric-name grammar minus the colon.
func TestValidLabelName(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		input string
		want  bool
	}
	tests := []tc{
		{"a plain key", "method", true},
		{"an underscore lead", "_shard", true},
		{"the double underscore is syntactically fine", "__name__", true},
		{"digits after the first byte", "code2", true},
		{"the reserved overflow key", "sdk_metric_overflow", true},
		{"the reserved bucket bound", "le", true},
		{"empty", "", false},
		{"a digit lead", "5xx", false},
		{"a colon — legal in a metric name, not here", "a:b", false},
		{"a dot", "http.method", false},
		{"a dash", "http-method", false},
		{"a quote", `a"b`, false},
		{"a multi-byte rune", "µ", false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := validLabelName(c.input); got != c.want {
				t.Errorf("validLabelName(%q) = %v, want %v", c.input, got, c.want)
			}
		})
	}
}

// TestPromFloatSpellsTheFormatsOwnExamples pins the value spelling against the
// exposition format documentation rather than against intuition.
//
// The format says a value is "a float represented as required by Go's
// ParseFloat()" and names NaN / +Inf / -Inf as valid. The two decimal cases are
// literal values from the documentation's own example document — `12.47` and
// the summary's `1.7560473e+07` — which is the check that matters: Go's
// FormatFloat('g', -1) switches to exponent notation on the same threshold the
// published bytes were produced with, so nothing here is a house convention.
func TestPromFloatSpellsTheFormatsOwnExamples(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		input float64
		want  string
	}
	tests := []tc{
		{"the documentation's minimalistic line", 12.47, "12.47"},
		{"the documentation's summary sum", 17560473, "1.7560473e+07"},
		{"the documentation's histogram sum", 53423, "53423"},
		{"a whole number keeps no decimal point", 1, "1"},
		{"a bucket bound", 0.05, "0.05"},
		{"zero", 0, "0"},
		{"not a number", math.NaN(), "NaN"},
		{"positive infinity", math.Inf(1), "+Inf"},
		{"negative infinity", math.Inf(-1), "-Inf"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := promFloat(c.input); got != c.want {
				t.Errorf("promFloat(%v) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

// TestPromFloatBoundsAreInjective pins that two distinct bucket bounds never
// format to the same `le` value.
//
// If they did, one histogram would emit two lines with the SAME le, which is a
// duplicate series in one family — the format rejects it, and a parser that
// does not would silently keep one of the two. The shortest-round-trip mode of
// FormatFloat is what guarantees it: distinct float64s have distinct shortest
// decimal forms by construction.
func TestPromFloatBoundsAreInjective(t *testing.T) {
	t.Parallel()
	bounds := []float64{
		0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
		1e6, 1e-9, math.SmallestNonzeroFloat64, math.MaxFloat64,
		1, math.Nextafter(1, 2),
	}
	seen := make(map[string]float64, len(bounds))
	for _, bound := range bounds {
		spelled := promFloat(bound)
		if prior, dup := seen[spelled]; dup && prior != bound {
			t.Errorf("%v and %v both spell as %q", prior, bound, spelled)
		}
		seen[spelled] = bound
	}
}

// TestAppendEscapedValueCoversTheFormat pins the escaping contract from both
// sides: the three sequences the format defines ARE emitted, and no fourth one
// is invented.
//
// The negative half is the load-bearing one. The 0.0.4 parser errors on an
// unknown escape sequence, so emitting `\r` or `\t` would cost the whole scrape
// rather than one label — strictly worse than leaving the byte alone, which
// cannot forge a line because only an unescaped newline terminates one.
func TestAppendEscapedValueCoversTheFormat(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		input string
		want  string
	}
	tests := []tc{
		{"nothing to escape", "GET", "GET"},
		{"empty is a legitimate value", "", ""},
		{"a backslash doubles", `\`, `\\`},
		{"a quote is escaped", `"`, `\"`},
		{"a newline becomes \\n", "\n", `\n`},
		{"the documentation's DOS path", `C:\DIR\FILE.TXT`, `C:\\DIR\\FILE.TXT`},
		{
			"the documentation's error message",
			"Cannot find file:\n\"FILE.TXT\"",
			`Cannot find file:\n\"FILE.TXT\"`,
		},
		{"a carriage return is NOT escaped", "\r", "\r"},
		{"a tab is NOT escaped", "\t", "\t"},
		{"a UTF-8 value passes through byte-wise", "héllo", "héllo"},
		{"a NUL passes through", "\x00", "\x00"},
		{
			//: the forged-line attempt: the newline is neutralised, so the
			//: injected text stays inside the quoted value.
			"a value engineered to forge a second series",
			"x\nevil_total 999",
			`x\nevil_total 999`,
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := string(appendEscapedValue(nil, c.input)); got != c.want {
				t.Errorf("appendEscapedValue(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

// TestCheckLabelNamesReservesLeOnlyForHistograms pins that "le" is refused on a
// bucket line — where it would be a duplicate label name — and accepted
// everywhere else, since a counter named with an `le` dimension is odd but
// perfectly representable.
func TestCheckLabelNamesReservesLeOnlyForHistograms(t *testing.T) {
	t.Parallel()
	labels := []coremetrics.AttrValue{attr(boundLabelKey, "1")}

	if err := checkAttrNames(labels, false); err != nil {
		t.Errorf("checkAttrNames(le, non-histogram) = %v, want nil", err)
	}
	if err := checkAttrNames(labels, true); err == nil {
		t.Error("checkAttrNames(le, histogram) = nil, want RESERVED_LABEL_NAME")
	}
}

// Test_Prometheus_DefaultsToStderr pins the destination of the Prometheus
// exporter this package registers at import time.
//
// ADR 0030's guard is a test per surface, and this is the third surface: no SDK
// default writes to os.Stdout. The temptation is real here in a way it is not
// for the text exporter — an exposition document LOOKS like something a caller
// would want on stdout — but a scrape endpoint hands the exporter its
// http.ResponseWriter and never touches the registered default, so nothing is
// gained by making the import dangerous.
func Test_Prometheus_DefaultsToStderr(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		got  io.Writer
		want io.Writer
	}
	//: reach through the interface to the concrete exporter's bound writer.
	pe, ok := Prometheus.(*prometheusExporter)
	//: the registered default must be this package's own implementation.
	if !ok {
		t.Fatalf("Prometheus is %T, want *prometheusExporter", Prometheus)
	}
	tests := []tc{
		{"registered default writes to stderr", pe.dst, os.Stderr},
		{"custom exporter honours its writer", newPrometheusExporter("custom", os.Stdout).dst, os.Stdout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: pointer identity — the exporter holds the stream, not a copy.
			if tc.got != tc.want {
				t.Errorf("dst = %s, want %s", streamName(tc.got), streamName(tc.want))
			}
		})
	}
}
