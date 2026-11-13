package seglog_test

import (
	"fmt"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/fs/seglog"
)

// benchPayload is the payload size every benchmark appends: a small
// record, so the cost read is the batch's syncs and not the copy.
const benchPayload = 128

// BenchmarkAppendSync is one batch of n records of 128 bytes appended and
// made durable by Sync: two syncs per batch whatever n, so the cost per
// record falls as the batch grows (#820's acceptance: 1, 16 and 256).
func BenchmarkAppendSync(b *testing.B) {
	for _, n := range []int{1, 16, 256} {
		b.Run(fmt.Sprintf("records=%d", n), func(b *testing.B) {
			l, err := seglog.Open(b.TempDir(), seglog.DefaultSegmentBytes)
			if err != nil {
				b.Fatal(err)
			}
			defer l.Close()
			p := make([]byte, benchPayload)
			var next uint64
			b.SetBytes(int64(n * benchPayload))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				for range n {
					if err := l.Append(next, 0, p); err != nil {
						b.Fatal(err)
					}
					next++
				}
				if err := l.Sync(); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(next), "ns/record")
		})
	}
}

// BenchmarkRange reads 256 durable records of 128 bytes with one Range:
// one read for the run, one parse and crc per record.
func BenchmarkRange(b *testing.B) {
	l, err := seglog.Open(b.TempDir(), seglog.DefaultSegmentBytes)
	if err != nil {
		b.Fatal(err)
	}
	defer l.Close()
	p := make([]byte, benchPayload)
	for i := range uint64(256) {
		if err := l.Append(i, 0, p); err != nil {
			b.Fatal(err)
		}
	}
	if err := l.Sync(); err != nil {
		b.Fatal(err)
	}
	dst := make([][]byte, 0, 256)
	b.SetBytes(256 * benchPayload)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		out, err := l.Range(0, 256, 1<<30, dst[:0])
		if err != nil || len(out) != 256 {
			b.Fatal(len(out), err)
		}
	}
}
