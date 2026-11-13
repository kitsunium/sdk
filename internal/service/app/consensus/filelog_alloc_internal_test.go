//go:build !race

// filelog_alloc_internal_test.go — the allocation gate of the file log's
// range read (#913). Race off, because the race detector allocates on every
// memory access: the alloc lane (tools/alloc-lane-targets.txt,
// //internal/service/app/consensus:consensus_alloc_test) is its only gate. The
// file is self-contained: that target compiles it with allocwindow_test.go
// alone.

package consensus

import (
	"bytes"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/fs/flock"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// entriesAllocRuns is how many reads a window counts.
const entriesAllocRuns int = 200

// entriesAllocCount and entriesAllocPayload are the fixture of ADR 0152 D15's
// row: 64 synced entries of 256 bytes.
const (
	entriesAllocCount   int = 64
	entriesAllocPayload int = 256
)

// entriesAllocSink keeps each read's entries reachable, so escape analysis
// cannot place the buffer on the stack.
var entriesAllocSink []coreconsensus.EntryValue

// syncedAllocLog is a file log holding entriesAllocCount synced entries of
// entriesAllocPayload bytes in one segment; it skips where a data directory
// is refused.
func syncedAllocLog(t *testing.T) *fileLog {
	t.Helper()
	if !flock.Native {
		t.Skip("no flock(2), no data directory (ADR 0144)")
	}
	d, err := NewDataDirWith(t.TempDir(), DataDirOptions{SegmentBytes: 4 << 20, RuntimeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	es := make([]coreconsensus.EntryValue, 0, entriesAllocCount)
	for i := range entriesAllocCount {
		data := bytes.Repeat([]byte{byte(i)}, entriesAllocPayload)
		es = append(es, coreconsensus.EntryValue{
			Position: coreconsensus.PositionValue{Term: 1, Index: uint64(i + 1)},
			Kind:     coreconsensus.EntryCommand, BodySum: wire.BodySum(data), Data: data,
		})
	}
	l := d.d.log
	if err := l.Append(es, nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	return l
}

// Entries over 64 synced entries of 256 B of one segment allocates at most
// twice: the read's buffer and the entry slice, grown once. Before #913 each
// entry was read with two preads into a buffer of its own. Mutation-checked:
// reading each record with readEntry instead of readRun was measured at 103
// allocations a call (20 600 over the 200 reads) and 128 preads.
func TestEntriesOfASyncedRunAllocateTheBufferAndTheSlice(t *testing.T) {
	l := syncedAllocLog(t)
	reads := 0
	l.readAt = func(f segFile, b []byte, off int64) (int, error) {
		reads++
		return f.ReadAt(b, off)
	}
	got := mallocsOver(entriesAllocRuns, func() {
		es, err := l.Entries(1, uint64(entriesAllocCount)+1, 1<<20, nil)
		if err != nil || len(es) != entriesAllocCount {
			t.Fatalf("Entries: %d entries, %v", len(es), err)
		}
		entriesAllocSink = es
	})
	reportWindow(t, "Entries of 64 x 256 B", got)
	if max := uint64(2 * entriesAllocRuns); got.own > max {
		t.Fatalf("%d reads of 64 x 256 B allocated %d times, over %d (2 a read)", entriesAllocRuns, got.own, max)
	}
	// The warm-up call counts too: one pread a call.
	if reads != entriesAllocRuns+1 {
		t.Fatalf("%d preads over %d reads, want one each", reads, entriesAllocRuns+1)
	}
	// Into a dst with room, the buffer alone.
	dst := make([]coreconsensus.EntryValue, 0, entriesAllocCount)
	got = mallocsOver(entriesAllocRuns, func() {
		es, err := l.Entries(1, uint64(entriesAllocCount)+1, 1<<20, dst[:0])
		if err != nil || len(es) != entriesAllocCount {
			t.Fatalf("Entries: %d entries, %v", len(es), err)
		}
		entriesAllocSink = es
	})
	reportWindow(t, "Entries of 64 x 256 B into a reused slice", got)
	if got.own > uint64(entriesAllocRuns) {
		t.Fatalf("%d reads into a reused slice allocated %d times, over one a read", entriesAllocRuns, got.own)
	}
}

// compactPrefixAt through a segment boundary drops the tables before it
// whole and reslices the one it falls in: nothing proportional to the
// entries kept is copied (#1111). The index's slot slice was cloned at
// every compaction, one allocation of 40 bytes per entry kept.
func TestACompactionThroughASegmentBoundaryCopiesNoEntries(t *testing.T) {
	if !flock.Native {
		t.Skip("no flock(2), no data directory (ADR 0144)")
	}
	d, err := NewDataDirWith(t.TempDir(), DataDirOptions{SegmentBytes: 64 << 10, RuntimeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	l := d.d.log
	data := bytes.Repeat([]byte{0xc4}, 1<<10)
	var next uint64 = 1
	for len(l.tables) < 8 {
		es := make([]coreconsensus.EntryValue, 0, 16)
		for range 16 {
			es = append(es, coreconsensus.EntryValue{
				Position: coreconsensus.PositionValue{Term: 1, Index: next},
				Kind:     coreconsensus.EntryCommand, BodySum: wire.BodySum(data), Data: data,
			})
			next++
		}
		if err := l.Append(es, nil); err != nil {
			t.Fatal(err)
		}
		if err := l.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	// Each compaction goes through the first entry of the next segment's
	// table: the one before goes whole, the next is resliced.
	throughs := []uint64{l.tables[1].first, l.tables[2].first, l.tables[3].first, l.tables[4].first}
	l.pending = make([]byte, 0, 1<<12)
	k := 0
	allocs := testing.AllocsPerRun(len(throughs)-1, func() {
		if err := l.compactPrefixAt(throughs[k], 0); err != nil {
			t.Fatal(err)
		}
		k++
	})
	t.Logf("a compaction through a segment boundary: %.1f allocs, %d entries kept", allocs, l.n)
	if first, _ := l.Bounds(); first != throughs[3]+1 {
		t.Fatalf("the log starts at %d, want %d", first, throughs[3]+1)
	}
	if allocs > 2 {
		t.Fatalf("a compaction through a segment boundary allocated %.1f times, over 2", allocs)
	}
}
