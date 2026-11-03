//go:build !race

package csv_test

import (
	"bytes"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/csv"
)

// TestUnmarshalSizesByRecordsNotNewlines is #1098 for csv: the outer slice is
// no longer sized from the newline count, which encoding/csv's own skipping
// of empty lines leaves unbounded over the records. 1 MiB of '\n' used to
// reserve 24 MiB of slice headers; it now allocates under 2 MiB and returns
// an empty table. Race off: the race runtime's allocations would blur the
// total.
func TestUnmarshalSizesByRecordsNotNewlines(t *testing.T) {
	data := bytes.Repeat([]byte{'\n'}, 1<<20)
	c := csv.New()
	var out [][]string
	if err := c.Unmarshal(data, &out); err != nil || len(out) != 0 {
		t.Fatalf("Unmarshal = %d records, %v; want 0, nil", len(out), err)
	}
	res := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var recs [][]string
			if err := c.Unmarshal(data, &recs); err != nil {
				b.Fatal(err)
			}
		}
	})
	t.Logf("allocated %d bytes per Unmarshal", res.AllocedBytesPerOp())
	if got := res.AllocedBytesPerOp(); got >= 2<<20 {
		t.Fatalf("Unmarshal of 1 MiB of newlines allocated %d bytes, want under 2 MiB", got)
	}
}
