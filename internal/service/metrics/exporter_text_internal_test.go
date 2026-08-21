package metrics

import (
	"bytes"
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
