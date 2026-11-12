package stream_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	svcstream "github.com/kitsunium/sdk/internal/service/data/stream"
)

// benchCompacted is how many records of 64 B a compaction benchmark writes
// over benchCompactKeys keys before it compacts them all.
const (
	benchCompacted   = 4096
	benchCompactKeys = 64
)

// BenchmarkCompact measures one compaction of a partition holding 4 096
// records of 64 B over 64 keys, every one old enough: 4 032 dropped, 64
// kept. The partition is written again before each iteration, outside the
// timer. On the file engine it is one segment of 1 MiB, rewritten whole.
func BenchmarkCompact(b *testing.B) {
	open := map[string]func(b *testing.B, clk clock.Timed, policy corestream.PolicyValue) corestream.Stream{
		"memory": func(b *testing.B, clk clock.Timed, policy corestream.PolicyValue) corestream.Stream {
			s, err := svcstream.NewMemory(svcstream.MemoryConfig{Clock: clk, Policy: policy})
			if err != nil {
				b.Fatal(err)
			}
			return s
		},
		"file": func(b *testing.B, clk clock.Timed, policy corestream.PolicyValue) corestream.Stream {
			requireFileEngine(b)
			policy.SegmentBytes = 1 << 20
			s, err := svcstream.NewFile(svcstream.FileConfig{Dir: b.TempDir(), Clock: clk, Policy: policy})
			if err != nil {
				b.Fatal(err)
			}
			return s
		},
	}
	records := benchBatch(benchCompacted, 64)
	for i := range records {
		records[i].Key = []byte("key-" + strconv.Itoa(i%benchCompactKeys))
	}
	for _, name := range []string{"memory", "file"} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				b.StopTimer()
				clk := clock.NewManualClock(epoch)
				s := open[name](b, clk, corestream.PolicyValue{CompactAfter: time.Minute})
				if _, err := s.Append(b.Context(), records); err != nil {
					b.Fatal(err)
				}
				clk.Advance(time.Minute)
				b.StartTimer()
				value, err := s.(corestream.Compactor).Compact(b.Context(), 0)
				if err != nil || value.Dropped != benchCompacted-benchCompactKeys {
					b.Fatalf("Compact() = %+v, %v", value, err)
				}
				b.StopTimer()
				closeBench(b, s)
				b.StartTimer()
			}
		})
	}
}

// closeBench closes a stream that is also an io.Closer.
func closeBench(b *testing.B, s corestream.Stream) {
	b.Helper()
	if closer, ok := s.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
