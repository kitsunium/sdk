package stream_test

import (
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/clock"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	svcstream "github.com/kitsunium/sdk/internal/service/data/stream"
)

// benchRetainBytes bounds what an Append benchmark keeps, so a run of
// millions of iterations measures the steady state — an append plus the
// retention it triggers — and not the heap growing without end.
const benchRetainBytes int64 = 64 << 20

// benchStream builds the memory engine for a benchmark.
func benchStream(b *testing.B, policy corestream.PolicyValue) corestream.Stream {
	b.Helper()
	s, err := svcstream.NewMemory(svcstream.MemoryConfig{Clock: clock.NewManualClock(epoch), Policy: policy})
	if err != nil {
		b.Fatalf("NewMemory() = %v", err)
	}
	return s
}

// benchBatch returns count unkeyed records of size payload bytes each.
func benchBatch(count, size int) []corestream.RecordValue {
	records := make([]corestream.RecordValue, count)
	for i := range records {
		records[i] = corestream.RecordValue{Payload: make([]byte, size)}
	}
	return records
}

// BenchmarkAppend measures one batch of 1, 16 or 256 records of 64 B or
// 4 KiB: the engine's copy of every byte, the offsets, and the wake.
func BenchmarkAppend(b *testing.B) {
	for _, size := range []int{64, 4096} {
		for _, count := range []int{1, 16, 256} {
			b.Run(strconv.Itoa(size)+"B/"+strconv.Itoa(count), func(b *testing.B) {
				s := benchStream(b, corestream.PolicyValue{RetainBytes: benchRetainBytes})
				records := benchBatch(count, size)
				b.SetBytes(int64(count * size))
				b.ReportAllocs()
				for b.Loop() {
					if _, err := s.Append(b.Context(), records); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkRead measures a read of 1 or 16 entries of 64 B from a partition
// holding 4 096: the reader's copy of every entry and byte.
func BenchmarkRead(b *testing.B) {
	const held = 4096
	s := benchStream(b, corestream.PolicyValue{})
	if _, err := s.Append(b.Context(), benchBatch(held, 64)); err != nil {
		b.Fatal(err)
	}
	for _, batch := range []int{1, 16} {
		b.Run(strconv.Itoa(batch), func(b *testing.B) {
			b.SetBytes(int64(batch * 64))
			b.ReportAllocs()
			from := corestream.Offset(0)
			for b.Loop() {
				entries, err := s.Read(b.Context(), 0, from, batch)
				if err != nil || len(entries) != batch {
					b.Fatalf("Read() = %d entries, %v", len(entries), err)
				}
				from = (from + corestream.Offset(batch)) % (held - corestream.Offset(batch))
			}
		})
	}
}

// BenchmarkPartitionOf measures the placement of one key — the hashing cost
// every keyed record pays — at three key lengths.
func BenchmarkPartitionOf(b *testing.B) {
	for _, size := range []int{8, 32, 128} {
		b.Run(strconv.Itoa(size)+"B", func(b *testing.B) {
			key := make([]byte, size)
			b.ReportAllocs()
			sink := 0
			for b.Loop() {
				sink += corestream.PartitionOf(key, 16)
			}
			_ = sink
		})
	}
}

// BenchmarkAppendKeyed sets a batch of 16 keyed records against the same
// batch unkeyed, on 16 partitions: the difference is the per-record hash and
// the check that every key names the batch's partition.
func BenchmarkAppendKeyed(b *testing.B) {
	for _, withKeys := range []bool{false, true} {
		name := "unkeyed"
		if withKeys {
			name = "keyed"
		}
		b.Run(name, func(b *testing.B) {
			s := benchStream(b, corestream.PolicyValue{Partitions: 16, RetainBytes: benchRetainBytes})
			records := benchBatch(16, 64)
			if withKeys {
				for i := range records {
					records[i].Key = []byte("order-000000042")
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.Append(b.Context(), records); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
