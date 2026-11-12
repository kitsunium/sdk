package stream_test

import (
	"hash/fnv"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/stream"
)

// TestPartitionOfIsFNV1aModuloThePartitions holds PartitionOf to hash/fnv's
// 32-bit FNV-1a, so the inlined arithmetic cannot drift from the hash every
// engine is documented to agree on (ADR 0231 §5).
func TestPartitionOfIsFNV1aModuloThePartitions(t *testing.T) {
	t.Parallel()
	keys := [][]byte{nil, {}, []byte("a"), []byte("order-42"), []byte("\x00\xff\x10"), make([]byte, 300)}
	for _, partitions := range []int{2, 3, 7, 16, 1000} {
		for _, key := range keys {
			h := fnv.New32a()
			_, _ = h.Write(key)
			want := int(uint64(h.Sum32()) % uint64(partitions))
			if got := stream.PartitionOf(key, partitions); got != want {
				t.Fatalf("PartitionOf(%q, %d) = %d, want %d", key, partitions, got, want)
			}
		}
	}
}

// TestPartitionOfBelowTwoPartitionsIsTheFirst pins the documented reading of
// a count below one, and of one, as the single partition 0.
func TestPartitionOfBelowTwoPartitionsIsTheFirst(t *testing.T) {
	t.Parallel()
	for _, partitions := range []int{-5, 0, 1} {
		if got := stream.PartitionOf([]byte("any"), partitions); got != 0 {
			t.Fatalf("PartitionOf(_, %d) = %d, want 0", partitions, got)
		}
	}
}

// TestPartitionOfStaysInRange checks every answer is a valid partition.
func TestPartitionOfStaysInRange(t *testing.T) {
	t.Parallel()
	for i := range 2000 {
		key := []byte{byte(i), byte(i >> 8)}
		if got := stream.PartitionOf(key, 13); got < 0 || got >= 13 {
			t.Fatalf("PartitionOf(%v, 13) = %d, out of [0, 13)", key, got)
		}
	}
}
