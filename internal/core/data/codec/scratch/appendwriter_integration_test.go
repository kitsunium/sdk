//go:build !race

package scratch_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
)

// appendSink keeps the probe's result alive.
var appendSink []byte

// TestAppendWriterRoundTripAllocatesNothing pins why the writer is pooled: a
// warm Acquire / Write / Release round trip into a dst with room allocates
// nothing, so an Append built on it pays only its encoder's allocations.
// Race off (AllocsPerRun counts the race runtime's own), no t.Parallel.
func TestAppendWriterRoundTripAllocatesNothing(t *testing.T) {
	dst := make([]byte, 0, 256)
	payload := []byte("a record of some forty bytes, written once")
	got := testing.AllocsPerRun(1000, func() {
		w := scratch.AcquireAppendWriter(dst[:0])
		_, _ = w.Write(payload)
		appendSink = w.Bytes()
		scratch.ReleaseAppendWriter(w)
	})
	if got != 0 {
		t.Fatalf("Acquire/Write/Release allocs/op = %v, want 0", got)
	}
}
