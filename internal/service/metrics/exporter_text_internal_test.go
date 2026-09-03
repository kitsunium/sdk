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

// Test_textExporter_ConcurrentExport pins the contract core/metrics.Exporter
// states outright — "Implementations MUST be safe for concurrent use".
//
// textExporter writes to a caller-supplied io.Writer. os.Stdout tolerates
// concurrent writes on most platforms, but the constructor accepts any writer,
// and a bytes.Buffer does not: unsynchronised concurrent Export calls race on
// its internal slice. Run under -race this test fails on the pre-mutex code.
func Test_textExporter_ConcurrentExport(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	e := newTextExporter("test", &buf)
	snap := coremetrics.SnapshotValue{
		Counters: map[string]int64{"requests": 7},
	}
	//: hammer the single writer from many goroutines at once.
	const writers int = 16
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			if err := e.Export(snap); err != nil {
				t.Errorf("Export: %v", err)
			}
		})
	}
	wg.Wait()
	//: every Export emitted exactly one line; none may be lost or torn.
	got := strings.Count(buf.String(), "counter requests 7\n")
	if got != writers {
		t.Errorf("got %d intact lines, want %d — writes interleaved or were lost", got, writers)
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
