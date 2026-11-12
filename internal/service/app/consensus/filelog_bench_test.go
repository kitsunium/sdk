package consensus

import (
	"strconv"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// BenchmarkFileLogEntries is the read side of ADR 0152 D15's 'segmented log'
// row: a fetch of synced entries, 64 or 4 096 of 256 B (the issue's 1 MiB
// fetch), read as Entries reads them since #913 — one pread a run of one
// segment — and as it read them before — readEntry, two preads an entry.
// It reports the cost per entry; BENCH.md holds the numbers.
func BenchmarkFileLogEntries(b *testing.B) {
	for _, n := range []int{64, 4096} {
		dir := b.TempDir()
		d, err := NewDataDirWith(dir, DataDirOptions{SegmentBytes: 64 << 20, RuntimeDir: b.TempDir()})
		if err != nil {
			b.Skipf("no data directory here: %v", err)
		}
		l := d.d.log
		if err := l.Append(sizedEnts(1, n, 256), nil); err != nil {
			b.Fatal(err)
		}
		if err := l.Sync(); err != nil {
			b.Fatal(err)
		}
		hi := uint64(n) + 1
		perEntry := func(b *testing.B, elapsed time.Duration) {
			b.ReportMetric(float64(elapsed.Nanoseconds())/float64(b.N)/float64(n), "ns/entry")
		}
		b.Run("range/"+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			var dst []coreconsensus.EntryValue
			start := time.Now()
			for b.Loop() {
				if dst, err = l.Entries(1, hi, 1<<30, dst[:0]); err != nil || len(dst) != n {
					b.Fatalf("Entries: %d, %v", len(dst), err)
				}
			}
			perEntry(b, time.Since(start))
		})
		b.Run("per-entry/"+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			dst := make([]coreconsensus.EntryValue, 0, n)
			start := time.Now()
			for b.Loop() {
				dst = dst[:0]
				l.mu.RLock()
				for i := l.first; i < l.first+l.n; i++ {
					e, rerr := l.readEntry(l.locate(i))
					if rerr != nil {
						l.mu.RUnlock()
						b.Fatal(rerr)
					}
					dst = append(dst, e)
				}
				l.mu.RUnlock()
			}
			perEntry(b, time.Since(start))
		})
		if err := d.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
