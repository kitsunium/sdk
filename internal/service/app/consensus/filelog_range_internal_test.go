package consensus

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// sizedEnts are n entries of payload bytes each from index from on, at term
// 1, each payload filled with its index's low byte.
func sizedEnts(from uint64, n, payload int) []coreconsensus.EntryValue {
	out := make([]coreconsensus.EntryValue, 0, n)
	for i := range n {
		idx := from + uint64(i)
		d := bytes.Repeat([]byte{byte(idx)}, payload)
		out = append(out, coreconsensus.EntryValue{
			Position: coreconsensus.PositionValue{Term: 1, Index: idx},
			Kind:     coreconsensus.EntryCommand, BodySum: wire.BodySum(d), Data: d,
		})
	}
	return out
}

// countPreads makes l count the preads its entry reads issue, and the bytes
// they ask for.
func countPreads(l *fileLog) (calls, asked *int) {
	calls, asked = new(int), new(int)
	l.readAt = func(f *os.File, b []byte, off int64) (int, error) {
		*calls++
		*asked += len(b)
		return f.ReadAt(b, off)
	}
	return calls, asked
}

// checkSized fails unless es are the entries from index from on that
// sizedEnts wrote, of payload bytes each.
func checkSized(t *testing.T, es []coreconsensus.EntryValue, from uint64, payload int) {
	t.Helper()
	for i, e := range es {
		idx := from + uint64(i)
		if e.Position.Index != idx || e.Position.Term != 1 || !bytes.Equal(e.Data, bytes.Repeat([]byte{byte(idx)}, payload)) {
			t.Fatalf("entry %d of the read: index %d term %d, %d bytes", i, e.Position.Index, e.Position.Term, len(e.Data))
		}
	}
}

// Once a batch is synced every read of its entries went through readEntry:
// two preads and one allocation per entry, about 8 000 syscalls for a fetch
// of 1 MiB of 256-byte entries (#913). A run of synced entries of one
// segment is one byte interval the slots already locate: it is read with
// ONE pread, maxBytes bounding what is read as well as what is returned,
// and parsed in place.
func TestEntriesReadASyncedRunOfOneSegmentInOnePread(t *testing.T) {
	const n, payload = 64, 256
	d := openDir(t, t.TempDir(), 4<<20)
	defer d.Close()
	l := d.d.log
	if err := l.Append(sizedEnts(1, n, payload), nil); err != nil {
		t.Fatal(err)
	}
	mustSync(t, l)
	calls, asked := countPreads(l)
	es, err := l.Entries(1, n+1, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != n || *calls != 1 {
		t.Fatalf("%d entries in %d preads, want %d in 1", len(es), *calls, n)
	}
	checkSized(t, es, 1, payload)
	if per := align8(recHeader + recFixed + payload); *asked != n*per {
		t.Fatalf("the read asked for %d bytes, want the %d records' %d", *asked, n, n*per)
	}
	// A bound of ten payloads: ten entries, and the read stops one record
	// past them at most, never at the run's end.
	*calls, *asked = 0, 0
	if es, err = l.Entries(1, n+1, 10*payload, es[:0]); err != nil {
		t.Fatal(err)
	}
	if len(es) != 10 || *calls != 1 {
		t.Fatalf("%d entries in %d preads under 10 payloads, want 10 in 1", len(es), *calls)
	}
	checkSized(t, es, 1, payload)
	if per := align8(recHeader + recFixed + payload); *asked > 11*per {
		t.Fatalf("a read of 10 entries asked for %d bytes, over 11 records' %d", *asked, 11*per)
	}
	// The first entry is returned above maxBytes, in one read of itself.
	*calls, *asked = 0, 0
	if es, err = l.Entries(5, n+1, 1, es[:0]); err != nil {
		t.Fatal(err)
	}
	if len(es) != 1 || *calls != 1 || *asked != align8(recHeader+recFixed+payload) {
		t.Fatalf("%d entries in %d preads of %d bytes under 1 byte, want the first alone", len(es), *calls, *asked)
	}
	checkSized(t, es, 5, payload)
}

// Entries alias the read's buffer, each capped at its own payload: an
// append to one copies it, never writes over the next record.
func TestAnEntryReadFromARunDoesNotReachIntoTheNext(t *testing.T) {
	d := openDir(t, t.TempDir(), 4<<20)
	defer d.Close()
	l := d.d.log
	if err := l.Append(sizedEnts(1, 4, 64), nil); err != nil {
		t.Fatal(err)
	}
	mustSync(t, l)
	es, err := l.Entries(1, 5, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 4 || cap(es[0].Data) != len(es[0].Data) {
		t.Fatalf("%d entries, the first's payload of %d bytes with room for %d", len(es), len(es[0].Data), cap(es[0].Data))
	}
	_ = append(es[0].Data, 0xee)
	checkSized(t, es, 1, 64)
}

// A run crosses the hard states and commits written between batches, and
// stops at a segment's end: entries spread over several segments and
// batches come back whole, in one pread per segment.
func TestEntriesReadOnePreadPerSegment(t *testing.T) {
	const payload = 1 << 10
	d := openDir(t, t.TempDir(), 64<<10)
	defer d.Close()
	l := d.d.log
	var idx uint64 = 1
	for b := range 12 {
		hs := &coreconsensus.HardState{Term: 1, Commit: idx - 1}
		if err := l.Append(sizedEnts(idx, 8, payload), hs); err != nil {
			t.Fatalf("batch %d: %v", b, err)
		}
		mustSync(t, l)
		idx += 8
	}
	first, last := l.Bounds()
	segs := map[uint64]bool{}
	for i := first; i <= last; i++ {
		segs[l.locate(i).seg] = true
	}
	if len(segs) < 2 {
		t.Fatalf("the entries sit in %d segment, want several", len(segs))
	}
	calls, _ := countPreads(l)
	es, err := l.Entries(first, last+1, 1<<30, nil)
	if err != nil {
		t.Fatal(err)
	}
	if uint64(len(es)) != last-first+1 || *calls != len(segs) {
		t.Fatalf("%d entries in %d preads, want %d in %d, one per segment", len(es), *calls, last-first+1, len(segs))
	}
	checkSized(t, es, first, payload)
}

// offsetOf is the offset field of an SDK error, -1 for none.
func offsetOf(err error) int64 {
	var e *errs.Error
	if !errors.As(err, &e) {
		return -1
	}
	for _, f := range e.Fields() {
		if f.Key() == "offset" {
			if v, perr := strconv.ParseInt(f.StringValue(), 10, 64); perr == nil {
				return v
			}
		}
	}
	return -1
}

// A record damaged inside a run is LOG_CORRUPT at its OWN offset, as
// readEntry reported it, whether the read starts before it or on it.
func TestADamagedRecordInsideARunIsCorruptAtItsOwnOffset(t *testing.T) {
	const n, payload, damaged = 16, 256, 9
	d := openDir(t, t.TempDir(), 4<<20)
	defer d.Close()
	l := d.d.log
	if err := l.Append(sizedEnts(1, n, payload), nil); err != nil {
		t.Fatal(err)
	}
	mustSync(t, l)
	s := l.locate(damaged)
	f, err := os.OpenFile(filepath.Join(l.logDir(), segName(s.seg)), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt([]byte{0xff}, s.off+int64(recHeader+recFixed)); err != nil {
		t.Fatal(err)
	}
	for _, lo := range []uint64{1, damaged} {
		_, err := l.Entries(lo, n+1, 1<<20, nil)
		if !errors.Is(err, coreconsensus.LogCorrupt) || offsetOf(err) != s.off {
			t.Fatalf("a read from %d over entry %d damaged at %d: %v, want LOG_CORRUPT at its offset", lo, damaged, s.off, err)
		}
	}
	// Before it, the run reads clean.
	es, err := l.Entries(1, damaged, 1<<20, nil)
	if err != nil || len(es) != damaged-1 {
		t.Fatalf("the %d entries before the damage: %d, %v", damaged-1, len(es), err)
	}
	checkSized(t, es, 1, payload)
}
