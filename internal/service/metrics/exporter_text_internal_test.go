package metrics

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// Test_newTextExporter pins the shared constructor: the writer it is handed is
// the writer it keeps, by identity. A copy would be meaningless for an
// io.Writer, and a default substituted here would silently redirect a caller's
// metrics somewhere they never asked for.
func Test_newTextExporter(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		key  coremetrics.ExporterName
		dst  io.Writer
	}
	var buf bytes.Buffer
	tests := []tc{
		{"a buffer", "test", &buf},
		{"standard error", "text", os.Stderr},
		{"standard output", "custom", os.Stdout},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		e := newTextExporter(c.key, c.dst)
		if e == nil {
			t.Fatal("newTextExporter returned nothing")
		}
		//: pointer identity — the exporter holds the stream, not a copy.
		if e.dst != c.dst {
			t.Errorf("the exporter bound a different writer")
		}
		if e.name != c.key {
			t.Errorf("name = %q, want %q", e.name, c.key)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_textExporter_Name pins the registry key the exporter answers to.
func Test_textExporter_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		key  coremetrics.ExporterName
	}
	tests := []tc{
		{"the default", textExporterName},
		{"a custom name", "prometheus-ish"},
		{"an empty name", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := newTextExporter(c.key, io.Discard).Name(); got != c.key {
			t.Errorf("Name() = %q, want %q", got, c.key)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_appendSeriesLine pins the one-series-per-line format. Every consumer of
// this output splits on newlines and then on spaces, so a missing separator
// merges two series into one unparseable line.
//
// The typed cases are the OTel attribute model made visible: a string is quoted
// and escaped, and a bool / integer / double is printed BARE, so a reader can
// see which kind a dimension carries. Every wire format flattens that away;
// this one is a diagnostic and does not have to.
func Test_appendSeriesLine(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		buf    []byte
		metric string
		attrs  []coremetrics.AttrValue
		value  string
		want   string
	}
	tests := []tc{
		{
			name: "onto an empty buffer", metric: "requests",
			value: "7", want: "requests 7\n",
		},
		{
			name: "onto an existing line", buf: []byte("a 1\n"),
			metric: "b", value: "2",
			want: "a 1\nb 2\n",
		},
		{
			//: a dimensionless series must not sprout empty braces.
			name:   "an empty value still terminates the line",
			metric: "x", value: "", want: "x \n",
		},
		{name: "an empty name", metric: "", value: "1", want: " 1\n"},
		{
			name: "one attribute", metric: "requests",
			attrs: []coremetrics.AttrValue{coremetrics.String("method", "GET")},
			value: "7", want: "requests{method=\"GET\"} 7\n",
		},
		{
			name:   "several attributes keep the snapshot's order",
			metric: "requests",
			attrs: []coremetrics.AttrValue{
				coremetrics.String("method", "GET"),
				coremetrics.String("status", "200"),
			},
			value: "7", want: "requests{method=\"GET\",status=\"200\"} 7\n",
		},
		{
			//: an empty VALUE is legitimate data and renders as empty quotes.
			name: "an empty string value", metric: "g",
			attrs: []coremetrics.AttrValue{coremetrics.String("tenant", "")},
			value: "1", want: "g{tenant=\"\"} 1\n",
		},
		{
			//: the three non-string kinds render unquoted, which is how the
			//: diagnostic keeps the type visible.
			name: "an integer attribute is bare", metric: "requests",
			attrs: []coremetrics.AttrValue{coremetrics.Int64("status", 503)},
			value: "1", want: "requests{status=503} 1\n",
		},
		{
			name: "a boolean attribute is bare", metric: "requests",
			attrs: []coremetrics.AttrValue{coremetrics.Bool("cached", true)},
			value: "1", want: "requests{cached=true} 1\n",
		},
		{
			name: "a double attribute is bare", metric: "requests",
			attrs: []coremetrics.AttrValue{coremetrics.Float64("ratio", 0.5)},
			value: "1", want: "requests{ratio=0.5} 1\n",
		},
		{
			//: the pair the wire would merge stays two distinguishable
			//: renderings here — quoted "1" against bare 1.
			name: "a string and an integer that spell the same", metric: "requests",
			attrs: []coremetrics.AttrValue{
				coremetrics.Int64("n", 1),
				coremetrics.String("s", "1"),
			},
			value: "1", want: "requests{n=1,s=\"1\"} 1\n",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		line := string(appendSeriesLine(c.buf, c.metric, c.attrs, c.value))
		if line != c.want {
			t.Errorf("appendSeriesLine = %q, want %q", line, c.want)
		}
		//: every line ends with a newline, or the next series would be glued
		//: to this one.
		if !strings.HasSuffix(line, "\n") {
			t.Errorf("appendSeriesLine did not terminate the line: %q", line)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_appendEscapedValue pins the escaping of a label VALUE, which is data.
//
// A value carries whatever the process is measuring — a URL, a header, a
// user-supplied tenant id. Unescaped, a value holding a quote or a newline
// forges a line that a reader of this output parses as another series: a
// metrics dump becomes an injection surface, and the forged series is
// indistinguishable from a real one.
func Test_appendEscapedValue(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		want string
	}
	tests := []tc{
		{"plain text passes through", "GET", "GET"},
		{"the empty value", "", ""},
		{"a quote cannot close the value", `a"b`, `a\"b`},
		{"a backslash doubles", `a\b`, `a\\b`},
		{"a newline cannot forge a line", "a\nb", `a\nb`},
		{
			//: the whole point, spelled out: a value that tries to close its
			//: own quote and open a fresh line stays one value.
			"a forged line stays one value",
			"x\" 1\ncounter forged 99",
			`x\" 1\ncounter forged 99`,
		},
		{"utf-8 is copied verbatim", "héllo", "héllo"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := string(appendEscapedValue(nil, c.in))
		if got != c.want {
			t.Errorf("appendEscapedValue(%q) = %q, want %q", c.in, got, c.want)
		}
		//: no escape may leave a raw newline behind, whatever the input.
		if strings.Contains(got, "\n") {
			t.Errorf("appendEscapedValue(%q) leaked a raw newline: %q", c.in, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_sortedKeys pins the ordering the whole format rests on. Go randomises map
// iteration deliberately, so without this the same snapshot would render
// differently on every run — and nothing downstream could be diffed.
func Test_sortedKeys(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   map[string][]coremetrics.SumValue
		want []string
	}
	//: the values are irrelevant here — only the key order is under test.
	group := func(names ...string) map[string][]coremetrics.SumValue {
		out := make(map[string][]coremetrics.SumValue, len(names))
		for _, n := range names {
			out[n] = []coremetrics.SumValue{{Value: 1}}
		}
		return out
	}
	tests := []tc{
		{"an empty map", group(), []string{}},
		{"a nil map", nil, []string{}},
		{"a single key", group("a"), []string{"a"}},
		{"keys are sorted", group("z", "a", "m"), []string{"a", "m", "z"}},
		{"digits sort before letters", group("a", "1"), []string{"1", "a"}},
		{"case is significant", group("b", "A"), []string{"A", "b"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := sortedKeys(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("sortedKeys = %v, want %v", got, c.want)
		}
		for i, want := range c.want {
			if got[i] != want {
				t.Errorf("key %d = %q, want %q", i, got[i], want)
			}
		}
		//: repeated calls agree, which is the property map iteration lacks.
		for i, again := range sortedKeys(c.in) {
			if again != got[i] {
				t.Errorf("a second call returned a different order at %d", i)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_textExporter_Export pins the contract core/metrics.Exporter states
// outright — "Implementations MUST be safe for concurrent use".
//
// textExporter writes to a caller-supplied io.Writer. os.Stdout tolerates
// concurrent writes on most platforms, but the constructor accepts any writer,
// and a bytes.Buffer does not: unsynchronised concurrent Export calls race on
// its internal slice. Under -race this fails on the pre-mutex code.
func Test_textExporter_Export(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		writers int
	}
	tests := []tc{
		{"a single writer", 1},
		{"sixteen concurrent writers", 16},
		{"sixty-four concurrent writers", 64},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var buf bytes.Buffer
		e := newTextExporter("test", &buf)
		snap := coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
			"requests": {
				Temporality: coremetrics.TemporalityCumulative,
				Monotonic:   true,
				Points:      []coremetrics.SumValue{{Value: 7}},
			},
		}}

		//: Goroutine lifecycle: c.writers goroutines, each exporting once and
		//: returning; the WaitGroup joins them all before the assertion.
		var wg sync.WaitGroup
		for range c.writers {
			wg.Go(func() {
				if err := e.Export(snap); err != nil {
					t.Errorf("Export = %v, want nil", err)
				}
			})
		}
		wg.Wait()

		//: every Export emitted exactly one INTACT line; a torn write would
		//: leave a partial line that matches nothing.
		got := strings.Count(buf.String(), "requests 7\n")
		if got != c.writers {
			t.Errorf("%d intact lines, want %d — writes interleaved or were lost", got, c.writers)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Text_DefaultsToStderr pins the destination of the exporter this package
// registers at import time.
//
// Importing a package must not arm a writer on a stream the process may be
// using as a protocol channel: a stdio JSON-RPC daemon, or any `cmd | jq`, is
// corrupted by one Export("text", ...) if the default is stdout — and the
// import that armed it is invisible at the call site. ADR 0030.
func Test_Text_DefaultsToStderr(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		got  io.Writer
		want io.Writer
	}
	//: reach through the interface to the concrete exporter's bound writer.
	te, ok := Text.(*textExporter)
	//: the registered default must be this package's own implementation.
	if !ok {
		t.Fatalf("Text is %T, want *textExporter", Text)
	}
	tests := []tc{
		{"registered default writes to stderr", te.dst, os.Stderr},
		{"custom exporter honours its writer", newTextExporter("custom", os.Stdout).dst, os.Stdout},
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

// streamName renders a standard stream by name so a failure reads as
// "dst = os.Stdout, want os.Stderr" instead of two pointer addresses.
func streamName(w io.Writer) string {
	//: the two streams this package can legitimately be bound to.
	switch w {
	case os.Stderr:
		//: the safe default.
		return "os.Stderr"
	case os.Stdout:
		//: the protocol channel — never a default.
		return "os.Stdout"
	}
	//: anything else is a caller-supplied writer.
	return fmt.Sprintf("%T", w)
}
