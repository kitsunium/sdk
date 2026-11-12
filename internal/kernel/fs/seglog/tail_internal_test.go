package seglog

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// baseSegBytes is the segment size of the log every tail case starts from.
const baseSegBytes int64 = 1 << 16

// baseLog is a log in a fresh directory holding records 0, 1 and 2 in one
// synced batch, closed; it returns the directory, the path of its one
// segment, the write offset there and the batch number the next batch takes.
func baseLog(t testing.TB) (dir, seg string, off int64, batch uint64) {
	t.Helper()
	dir = t.TempDir()
	l, err := Open(dir, baseSegBytes)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(0); i < 3; i++ {
		if err := l.Append(i, 9, []byte(fmt.Sprintf("entry-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	seg, off, batch = l.segPath(l.segs[len(l.segs)-1].n), l.off, l.batch
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, seg, off, batch
}

// entryRec is the encoded entry record of index in batch.
func entryRec(batch, index uint64, end bool) []byte {
	return appendRecord(nil, &record{kind: recEntry, tag: 9, batch: batch, index: index, end: end, data: []byte("x")})
}

// writeAt writes b at off in the file at path, as a crash would have left it.
func writeAt(t testing.TB, path string, b []byte, off int64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt(b, off); err != nil {
		t.Fatal(err)
	}
}

// published reads every published segment of dir, by name.
func published(t testing.TB, dir string) map[string][]byte {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "seg-????????????????"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(n)] = b
	}
	return out
}

// tailVerdict opens dir and holds the open to one of the two answers a hole
// may get: a torn tail, repaired — the records before it kept and every
// byte from the write position to the segment's end zero on disk — or
// damage, refused with the segment files left exactly as they were. It
// returns the opened log's next index, or the refusal.
func tailVerdict(t testing.TB, dir, seg string) (uint64, error) {
	t.Helper()
	before := published(t, dir)
	l, err := Open(dir, baseSegBytes)
	if err != nil {
		if !errs.HasCode(err, CodeDamaged) && !errs.HasCode(err, CodeVersionUnsupported) {
			t.Fatalf("open refused with %v, neither damage nor a newer layout", err)
		}
		after := published(t, dir)
		if len(after) != len(before) {
			t.Fatalf("a refused open published or removed a segment: %d then %d", len(before), len(after))
		}
		for n, b := range before {
			if !bytes.Equal(b, after[n]) {
				t.Fatalf("segment %s changed by a refused open", n)
			}
		}
		return 0, err
	}
	defer l.Close()
	b, rerr := os.ReadFile(seg)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if l.tornTails > 0 && !allZero(b[tailOff(l, seg):]) {
		t.Fatalf("a repaired tail left non-zero bytes past the write position %d", tailOff(l, seg))
	}
	_, next := l.Bounds()
	for i := uint64(0); i < min(next, 3); i++ {
		got, err := l.Read(i)
		if err != nil || string(got) != fmt.Sprintf("entry-%d", i) {
			t.Fatalf("record %d after a repair: %q %v", i, got, err)
		}
	}
	if next < 3 {
		t.Fatalf("a repair dropped synced records: next %d", next)
	}
	return next, nil
}

// tailOff is the write position of l in the segment at seg when it is
// still the segment being written, else its whole size.
func tailOff(l *Log, seg string) int64 {
	last := l.segs[len(l.segs)-1]
	if l.segPath(last.n) == seg {
		return l.off
	}
	return last.size
}

// TestATailIsTornOrDamaged ports the tail cases of the consensus file log
// (filelog_internal_test.go) to the primitive: the clean write position,
// an unfinished highest batch (TestRecoveryTruncatesTheUnfinishedBatch),
// garbage after a complete batch (TestRecoveryTruncatesGarbageAfterACompleteBatch),
// a length near 2^31 (TestARecordOfLength2To31IsAHole) — all torn, repaired —
// and a hole before a later batch (TestRecoveryQuarantinesAHoleBeforeALaterBatch),
// damage inside a batch whose end record is valid
// (TestRecoveryQuarantinesDamageInACompletedBatch), a newer layout's record
// behind a hole (#511) and a hole in an earlier segment
// (TestAnEarlierSegmentIsJudgedOnEveryRecordItHeld) — all refused, unchanged.
func TestATailIsTornOrDamaged(t *testing.T) {
	type tc struct {
		name string
		// write damages the base log at seg, whose write position is off and
		// whose next batch is b.
		write func(t *testing.T, dir, seg string, off int64, b uint64)
		next  uint64
		code  errs.Code
	}
	cases := []tc{
		{"the clean write position", func(*testing.T, string, string, int64, uint64) {}, 3, 0},
		{"an unfinished highest batch", func(t *testing.T, _, seg string, off int64, b uint64) {
			r := entryRec(b, 3, false)
			writeAt(t, seg, r, off)
			writeAt(t, seg, []byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4}, off+int64(len(r)))
		}, 3, 0},
		{"garbage after a complete batch", func(t *testing.T, _, seg string, off int64, _ uint64) {
			writeAt(t, seg, []byte("garbage-garbage-garbage-garbage!"), off)
		}, 3, 0},
		{"a length near 2^31", func(t *testing.T, _, seg string, off int64, _ uint64) {
			writeAt(t, seg, []byte{0xff, 0xff, 0xff, 0x7f}, off)
		}, 3, 0},
		{"a hole before a later batch", func(t *testing.T, _, seg string, off int64, b uint64) {
			k := entryRec(b, 3, true)
			writeAt(t, seg, k, off)
			writeAt(t, seg, entryRec(b+1, 4, true), off+int64(len(k)))
			writeAt(t, seg, make([]byte, 8), off)
		}, 0, CodeDamaged},
		{"damage inside a batch whose end record is valid", func(t *testing.T, _, seg string, off int64, b uint64) {
			r1 := entryRec(b, 3, false)
			writeAt(t, seg, r1, off)
			writeAt(t, seg, entryRec(b, 4, true), off+int64(len(r1)))
			writeAt(t, seg, []byte{0xff}, off+int64(recHeader+recFixed))
		}, 0, CodeDamaged},
		{"a newer layout's record behind a hole", func(t *testing.T, _, seg string, off int64, b uint64) {
			r := entryRec(b, 3, true)
			r[recHeader] = recVersion + 1
			sealRecord(r)
			writeAt(t, seg, r, off+64)
		}, 0, CodeVersionUnsupported},
		{"a hole in an earlier segment", func(t *testing.T, dir, _ string, _ int64, _ uint64) {
			l, err := Open(dir, baseSegBytes)
			if err != nil {
				t.Fatal(err)
			}
			for i := uint64(3); i < 3+2000; i++ {
				if err := l.Append(i, 9, bytes.Repeat([]byte{1}, 64)); err != nil {
					t.Fatal(err)
				}
				if i%50 == 0 {
					if err := l.Sync(); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := l.Sync(); err != nil {
				t.Fatal(err)
			}
			if len(l.segs) < 2 {
				t.Fatalf("%d segment(s), want a rotation", len(l.segs))
			}
			first := l.segPath(l.segs[0].n)
			if err := l.Close(); err != nil {
				t.Fatal(err)
			}
			writeAt(t, first, []byte{0xff}, 2048)
		}, 0, CodeDamaged},
	}
	runCase := func(t *testing.T, c tc) {
		dir, seg, off, b := baseLog(t)
		c.write(t, dir, seg, off, b)
		next, err := tailVerdict(t, dir, seg)
		if c.code != 0 {
			if !errs.HasCode(err, c.code) {
				t.Fatalf("open = %v %v, want %v", err, errs.FieldsOf(err), c.code)
			}
			return
		}
		if err != nil {
			t.Fatalf("open = %v %v, want a repair", err, errs.FieldsOf(err))
		}
		if next != c.next {
			t.Fatalf("next %d, want %d", next, c.next)
		}
		// The log keeps working after the repair, and the repair holds.
		l, err := Open(dir, baseSegBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Append(next, 1, []byte("after")); err != nil {
			t.Fatal(err)
		}
		if err := l.Sync(); err != nil {
			t.Fatal(err)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		l, err = Open(dir, baseSegBytes)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		if got, err := l.Read(next); err != nil || string(got) != "after" {
			t.Fatalf("the record after the repair: %q %v", got, err)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// A crash after the seal of segment N and before the rename of N+1: the
// first open finishes the rotation, and the log it leaves opens again and
// keeps its records across a third open.
func TestASealWithoutItsNextSegmentSurvivesTwoOpens(t *testing.T) {
	dir, seg, off, b := baseLog(t)
	writeAt(t, seg, appendRecord(nil, &record{kind: recSeal, end: true, batch: b, index: uint64(off)}), off)
	l, err := Open(dir, baseSegBytes)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(l.segs); n != 2 {
		t.Fatalf("%d segments after the first open, want 2", n)
	}
	for i := uint64(3); i < 6; i++ {
		if err := l.Append(i, 9, []byte(fmt.Sprintf("entry-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		if l, err = Open(dir, baseSegBytes); err != nil {
			t.Fatal(err)
		}
		for i := uint64(0); i < 6; i++ {
			if got, err := l.Read(i); err != nil || string(got) != fmt.Sprintf("entry-%d", i) {
				t.Fatalf("record %d: %q %v", i, got, err)
			}
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

// The end record of a batch is written only once the rest of the batch is
// durable: two syncs for a batch of several records, the first before any
// end record is on the disk.
func TestTheEndRecordIsWrittenOnceTheRestOfItsBatchIsDurable(t *testing.T) {
	l, err := Open(t.TempDir(), baseSegBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := uint64(0); i < 3; i++ {
		if err := l.Append(i, 0, []byte("abc")); err != nil {
			t.Fatal(err)
		}
	}
	batch, off := l.batch, l.off
	seg := l.segPath(l.segs[len(l.segs)-1].n)
	type seen struct{ recs, ends int }
	var syncs []seen
	l.duringSync = func() {
		b, err := os.ReadFile(seg)
		if err != nil {
			t.Error(err)
			return
		}
		var s seen
		for o := int(off); o+recHeader+recFixed <= len(b); {
			r, ok := parseRecord(b[o:])
			if !ok || r.batch != batch {
				break
			}
			s.recs++
			if r.end {
				s.ends++
			}
			o += r.length
		}
		syncs = append(syncs, s)
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	if len(syncs) != 2 || syncs[0] != (seen{2, 0}) || syncs[1] != (seen{3, 1}) {
		t.Fatalf("the disk at each sync: %+v, want {2 0} then {3 1}", syncs)
	}
}

// A sealed segment is read up to its seal, not to its preallocated size.
func TestASealedSegmentIsReadUpToItsSeal(t *testing.T) {
	const segBytes = 4 << 20
	dir := t.TempDir()
	l, err := Open(dir, segBytes)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(0); i < 3; i++ {
		if err := l.Append(i, 0, []byte("abc")); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	seal := appendRecord(nil, &record{kind: recSeal, end: true, batch: l.batch, index: uint64(l.off)})
	sealEnd := l.off + int64(len(seal))
	seg := l.segPath(1)
	writeAt(t, seg, seal, l.off)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := scanSegment(dir, 1, &scanBufs{})
	if err != nil {
		t.Fatal(err)
	}
	if !s.sealed || s.size != segBytes {
		t.Fatalf("sealed %v, size %d", s.sealed, s.size)
	}
	if read := int64(len(s.data)); read >= sealEnd+scanChunk || read >= s.size {
		t.Fatalf("a segment of %d sealed at %d read %d bytes", s.size, sealEnd, read)
	}
}

// A record whose length field is near 2^31 is a hole, never a panic: on 32
// bits the header sum wrapped negative and the slice panicked.
func TestARecordOfLength2To31IsAHole(t *testing.T) {
	for _, n := range []uint32{0x7FFFFFF8, 0x7FFFFFFF, 1 << 31, 0xFFFFFFFF} {
		b := make([]byte, 256)
		binary.LittleEndian.PutUint32(b, n)
		if body := sealedBody(b); body != nil {
			t.Fatalf("length %#x: a body of %d bytes", n, len(body))
		}
		if _, ok := parseRecord(b); ok {
			t.Fatalf("length %#x parsed as a record", n)
		}
		if err := unknownVersionAt(b, 1, 0); err != nil {
			t.Fatalf("length %#x: %v", n, err)
		}
	}
}
