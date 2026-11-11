package trace_test

import (
	"strings"
	"testing"

	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/service/observe/trace"
)

// traceCorpus holds a valid value and its broken neighbours of every width.
func traceCorpus() []string {
	const (
		traceID = "0af7651916cd43dd8448eb211c80319c"
		spanID  = "b7ad6b7169203331"
		valid   = "00-" + traceID + "-" + spanID + "-01"
	)
	all := []string{
		valid,
		"ff-" + traceID + "-" + spanID + "-01",
		"00-" + strings.Repeat("0", 32) + "-" + spanID + "-01",
		"00-" + traceID + "-" + strings.Repeat("0", 16) + "-01",
		"00-" + strings.ToUpper(traceID) + "-" + spanID + "-01",
		"0A-" + traceID + "-" + spanID + "-01",
		"00-" + traceID + "-" + spanID + "-0g",
		"00_" + traceID + "-" + spanID + "-01",
		"01-" + traceID + "-" + spanID + "-01-ext",
		"01-" + traceID + "-" + spanID + "-01",
		"01-" + traceID + "-" + spanID + "-01x",
		valid + "-trailing",
		valid + "x",
		"",
	}
	//: every length around the grammar's, truncated and repeated.
	for n := 0; n <= 64; n++ {
		all = append(all, strings.Repeat("a", n), valid[:min(n, len(valid))])
	}
	return all
}

// TestParseTraceParentBytes_agreesWithTheStringParser pins that the byte parser
// is the string parser's rules: one answer on every value of the corpus.
func TestParseTraceParentBytes_agreesWithTheStringParser(t *testing.T) {
	t.Parallel()
	for _, value := range traceCorpus() {
		want, err := trace.ParseTraceParent(value)
		got, ok := coretrace.ParseTraceParentBytes([]byte(value))
		if ok != (err == nil) {
			t.Fatalf("%q: bytes ok=%v, string err=%v", value, ok, err)
		}
		if ok && (got.TraceID != want.TraceID || got.SpanID != want.SpanID || got.Flags != want.Flags || got.Remote != want.Remote) {
			t.Fatalf("%q: bytes %+v, string %+v", value, got, want)
		}
	}
}

// TestAppendTraceParent_isFormatTraceParent pins the writer on every valid
// value of the corpus: the same bytes as the string formatter.
func TestAppendTraceParent_isFormatTraceParent(t *testing.T) {
	t.Parallel()
	for _, value := range traceCorpus() {
		sc, ok := coretrace.ParseTraceParentBytes([]byte(value))
		if !ok {
			continue
		}
		want, wok := trace.FormatTraceParent(sc)
		if got := coretrace.AppendTraceParent(nil, sc); !wok || string(got) != want {
			t.Fatalf("%q: Append = %q, Format = %q (%v)", value, got, want, wok)
		}
	}
}
