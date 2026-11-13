package consensus

import (
	"bytes"
	"math/rand/v2"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The index kept a 40-byte slot per retained entry, a tenth of the log with
// 256-byte entries, outside MemoryBudget (#1111). It keeps 4 bytes a synced
// entry now, in a table per segment, and a run per term: 1 048 576 entries
// of 256 B on 64 KiB segments, 32 MiB at 32 bytes an entry, hold under
// 5 MiB. The entries go through Append and the settling of each batch a
// Sync makes, segment after segment of 64 KiB as rotate fills them; the
// writes and the fdatasyncs are left out, which change nothing the index
// holds: a million entries over 5 800 segments cost minutes of syncs.
func TestTheIndexHoldsFourBytesAnEntry(t *testing.T) {
	const total, payload, batch, segBytes = 1 << 20, 256, 180, 64 << 10
	d := openDir(t, t.TempDir(), segBytes)
	defer d.Close()
	l := d.d.log
	data := bytes.Repeat([]byte{0x5a}, payload)
	sum := wire.BodySum(data)
	cpLen := int64(align8(recHeader + recFixed + checkpointLen))
	seg := l.segs[len(l.segs)-1].n
	es := make([]coreconsensus.EntryValue, 0, batch)
	for next := uint64(1); next <= total; {
		es = es[:0]
		for ; len(es) < batch && next <= total; next++ {
			es = append(es, coreconsensus.EntryValue{
				Position: coreconsensus.PositionValue{Term: 1 + next>>18, Index: next},
				Kind:     coreconsensus.EntryCommand, BodySum: sum, Data: data,
			})
		}
		if err := l.Append(es, nil); err != nil {
			t.Fatal(err)
		}
		l.mu.Lock()
		if l.off+int64(len(l.pending))+sealReserve > segBytes {
			seg++
			l.off = cpLen
		}
		ok := l.settleBatch(seg)
		l.mu.Unlock()
		if !ok {
			t.Fatal("the index refused a batch's offsets")
		}
	}
	if first, last := l.Bounds(); first != 1 || last != total {
		t.Fatalf("bounds [%d, %d], want [1, %d]", first, last, total)
	}
	if len(l.mem) != 0 || len(l.tables) < 5000 {
		t.Fatalf("%d entries left in memory, %d tables", len(l.mem), len(l.tables))
	}
	got := l.indexBytes()
	t.Logf("%d entries in %d segments: the index holds %d bytes, %.2f an entry, %d term runs",
		total, len(l.tables), got, float64(got)/total, len(l.runs))
	if got >= 5<<20 {
		t.Fatalf("the index of %d entries holds %d bytes, not under 5 MiB", total, got)
	}
	for _, i := range []uint64{1, 1 << 18, 1<<18 + 1, total / 2, total} {
		if term, err := l.Term(i); err != nil || term != 1+i>>18 {
			t.Fatalf("Term(%d) = %d, %v, want %d", i, term, err, 1+i>>18)
		}
	}
	// Every entry is located where its batch put it, segment by segment.
	prev := entryLoc{}
	for i := uint64(1); i <= total; i++ {
		at := l.locate(i)
		if at.seg < prev.seg || (at.seg == prev.seg && at.off <= prev.off) {
			t.Fatalf("entry %d at %v, after entry %d at %v", i, at, i-1, prev)
		}
		prev = at
	}
}

// Term(i) answers what the memory log answers, for every retained index and
// for first-1, across 1 000 elections: each one appends a few entries of its
// term, may truncate a suffix written under an earlier one — in the batch
// still pending or on disk —, and may compact; and once more after a reopen,
// which rebuilds the index from the records (#1111).
func TestTermAgreesWithTheMemoryLogAcrossElections(t *testing.T) {
	rng := rand.New(rand.NewPCG(1111, 7))
	dir := t.TempDir()
	d := openDir(t, dir, 16<<10)
	l := d.d.log
	m := NewMemoryLog()
	term := uint64(1)
	both := func(name string, f func(coreconsensus.LogStore) error) {
		t.Helper()
		if err, merr := f(l), f(m); err != nil || merr != nil {
			t.Fatalf("%s: file %v, memory %v", name, err, merr)
		}
	}
	for range 1000 {
		_, last := m.Bounds()
		ents := sizedTermEnts(last+1, 1+rng.IntN(12), 8+rng.IntN(200), term)
		both("append", func(s coreconsensus.LogStore) error { return s.Append(ents, nil) })
		if rng.IntN(3) == 0 {
			// A new leader's log disagrees past some point: the follower
			// truncates there, synced or not, and writes the new term's.
			term++
			first, last := m.Bounds()
			from := first + rng.Uint64N(last-first+2)
			both("truncate", func(s coreconsensus.LogStore) error { return s.TruncateSuffix(from) })
			ents = sizedTermEnts(from, 1+rng.IntN(6), 8+rng.IntN(200), term)
			both("append", func(s coreconsensus.LogStore) error { return s.Append(ents, nil) })
		}
		if rng.IntN(4) == 0 {
			both("sync", func(s coreconsensus.LogStore) error { return s.Sync() })
		}
		if first, last := m.Bounds(); last-first > 400 {
			through := first + rng.Uint64N(last-first-100)
			both("compact", func(s coreconsensus.LogStore) error { return s.CompactPrefix(through) })
		}
		term++
		sameTerms(t, l, m)
	}
	mustSync(t, l)
	sameEntries(t, l, m)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d = openDir(t, dir, 16<<10)
	defer d.Close()
	sameTerms(t, d.d.log, m)
	sameEntries(t, d.d.log, m)
}

// sizedTermEnts are n entries of payload bytes from index from on, at term
// term, each payload filled with its index's low byte.
func sizedTermEnts(from uint64, n, payload int, term uint64) []coreconsensus.EntryValue {
	es := sizedEnts(from, n, payload)
	for i := range es {
		es[i].Position.Term = term
	}
	return es
}

// sameTerms fails unless l and m hold the same bounds and answer the same
// term for first-1 and every retained index.
func sameTerms(t *testing.T, l, m coreconsensus.LogReader) {
	t.Helper()
	first, last := m.Bounds()
	if f, la := l.Bounds(); f != first || la != last {
		t.Fatalf("bounds [%d, %d], the memory log's [%d, %d]", f, la, first, last)
	}
	for i := first - 1; i <= last; i++ {
		want, merr := m.Term(i)
		got, err := l.Term(i)
		if got != want || (err == nil) != (merr == nil) {
			t.Fatalf("Term(%d) = %d, %v; the memory log's %d, %v", i, got, err, want, merr)
		}
	}
}

// sameEntries fails unless l reads back every entry m retains, position and
// payload.
func sameEntries(t *testing.T, l, m coreconsensus.LogReader) {
	t.Helper()
	first, last := m.Bounds()
	want, err := m.Entries(first, last+1, 1<<30, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := l.Entries(first, last+1, 1<<30, nil)
	if err != nil || len(got) != len(want) {
		t.Fatalf("%d entries, %v; the memory log holds %d", len(got), err, len(want))
	}
	for i := range want {
		if got[i].Position != want[i].Position || !bytes.Equal(got[i].Data, want[i].Data) {
			t.Fatalf("entry %d: %v of %d bytes, the memory log's %v of %d", i, got[i].Position, len(got[i].Data), want[i].Position, len(want[i].Data))
		}
	}
}

// MemoryBudget counts the file log's index: 4 bytes for each record of 56,
// an empty payload's, MaxLogBytes can hold (#1111).
func TestMemoryBudgetCountsTheIndex(t *testing.T) {
	c := Config{MaxLogBytes: 56 << 20, MaxPendingBytes: 1, MaxApplyLagBytes: 2, RecentEntriesBytes: 3, MaxInflightBytes: 4}
	if got, want := c.MemoryBudget(2), int64(1+2+3+2*4+4<<20); got != want {
		t.Fatalf("MemoryBudget(2) = %d, want %d", got, want)
	}
	if minRecordBytes != 56 {
		t.Fatalf("the smallest record is %d bytes, the doc says 56", minRecordBytes)
	}
}
