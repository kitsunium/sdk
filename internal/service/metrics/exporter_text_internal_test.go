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

// Test_appendLine pins the one-metric-per-line format. Every consumer of this
// output splits on newlines and then on spaces, so a missing separator merges
// two metrics into one unparseable line.
func Test_appendLine(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		buf    []byte
		prefix string
		metric string
		value  string
		want   string
	}
	tests := []tc{
		{"onto an empty buffer", nil, "counter ", "requests", "7", "counter requests 7\n"},
		{
			"onto an existing line",
			[]byte("counter a 1\n"), "gauge ", "b", "2",
			"counter a 1\ngauge b 2\n",
		},
		{"an empty value still terminates the line", nil, "counter ", "x", "", "counter x \n"},
		{"an empty name", nil, "counter ", "", "1", "counter  1\n"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		line := string(appendLine(c.buf, c.prefix, c.metric, c.value))
		if line != c.want {
			t.Errorf("appendLine = %q, want %q", line, c.want)
		}
		//: every line ends with a newline, or the next metric would be glued
		//: to this one.
		if !strings.HasSuffix(line, "\n") {
			t.Errorf("appendLine did not terminate the line: %q", line)
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
		in   map[string]int64
		want []string
	}
	tests := []tc{
		{"an empty map", map[string]int64{}, []string{}},
		{"a nil map", nil, []string{}},
		{"a single key", map[string]int64{"a": 1}, []string{"a"}},
		{"keys are sorted", map[string]int64{"z": 1, "a": 2, "m": 3}, []string{"a", "m", "z"}},
		{"digits sort before letters", map[string]int64{"a": 1, "1": 2}, []string{"1", "a"}},
		{"case is significant", map[string]int64{"b": 1, "A": 2}, []string{"A", "b"}},
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
		snap := coremetrics.SnapshotValue{Counters: map[string]int64{"requests": 7}}

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
		got := strings.Count(buf.String(), "counter requests 7\n")
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
