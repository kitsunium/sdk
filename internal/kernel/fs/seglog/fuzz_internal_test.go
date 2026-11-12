package seglog

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// FuzzParseRecord: the record decoder never panics, and a record it accepts
// never claims more bytes than it was given.
func FuzzParseRecord(f *testing.F) {
	f.Add(appendRecord(nil, &record{kind: recEntry, batch: 1, index: 1, data: []byte("x"), end: true}))
	f.Fuzz(func(t *testing.T, b []byte) {
		r, ok := parseRecord(b)
		if ok && r.length > len(b)+recAlign-1 {
			t.Fatalf("record of %d bytes from %d", r.length, len(b))
		}
	})
}

// tailSeeds are the tails TestATailIsTornOrDamaged writes after the base
// log's last batch, b standing for its next batch number: the eight cases'
// shapes, as bytes.
func tailSeeds(b uint64) [][]byte {
	cat := func(parts ...[]byte) []byte { return slices.Concat(parts...) }
	newer := entryRec(b, 3, true)
	newer[recHeader] = recVersion + 1
	sealRecord(newer)
	hole := cat(make([]byte, 8), entryRec(b, 3, true)[8:], entryRec(b+1, 4, true))
	flipped := cat(entryRec(b, 3, false), entryRec(b, 4, true))
	flipped[recHeader+recFixed] ^= 0xff
	return [][]byte{
		nil,
		cat(entryRec(b, 3, false), []byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4}),
		[]byte("garbage-garbage-garbage-garbage!"),
		{0xff, 0xff, 0xff, 0x7f},
		hole,
		flipped,
		cat(make([]byte, 64), newer),
		cat(entryRec(b, 3, false), entryRec(b, 4, true)),
	}
}

// FuzzOpenTail: whatever bytes a crash left after the last synced batch of
// a segment, Open never panics and classifies every hole either as a torn
// tail — repaired, the synced records kept and every byte past the write
// position zero on disk — or as damage or a newer layout, refused with the
// segment left exactly as it was (tailVerdict). The seeds are the shapes of
// TestATailIsTornOrDamaged's cases.
func FuzzOpenTail(f *testing.F) {
	_, _, _, b := baseLog(f)
	for _, s := range tailSeeds(b) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, tail []byte) {
		dir, seg, off, _ := baseLog(t)
		if int64(len(tail)) > baseSegBytes-off {
			tail = tail[:baseSegBytes-off]
		}
		writeAt(t, seg, tail, off)
		if _, err := tailVerdict(t, dir, seg); err != nil {
			t.Logf("refused: %v %v", err, errs.FieldsOf(err))
		}
	})
}

// FuzzScanSegment: a segment scanned in pieces, into a buffer that held
// another segment, gives what a parse of the whole file gives: the same
// records, hole, seal and verdict, and bytes read that are the file's, all
// of them for a hole.
func FuzzScanSegment(f *testing.F) {
	for _, s := range scanSeeds() {
		f.Add(s.prev, s.b, s.chunk)
	}
	f.Fuzz(func(t *testing.T, prev, b []byte, chunk uint16) {
		dir := t.TempDir()
		for i, seg := range [][]byte{prev, b} {
			if err := os.WriteFile(filepath.Join(dir, segName(uint64(i+1))), seg, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		sb := scanBufs{chunk: int64(chunk%4096) + 1}
		// Whatever its verdict, the first segment leaves its bytes in the
		// buffer the second is read into.
		if _, err := scanSegment(dir, 1, &sb); err != nil {
			t.Logf("first segment: %v", err)
		}
		got, err := scanSegment(dir, 2, &sb)
		want, wantErr := scanWhole(b, 2)
		if (err == nil) != (wantErr == nil) || (err != nil && err.Error() != wantErr.Error()) {
			t.Fatalf("scan in pieces: %v, of the whole file: %v", err, wantErr)
		}
		if err != nil {
			return
		}
		if !slices.Equal(got.recs, want.recs) || got.hole != want.hole || got.sealed != want.sealed || got.size != want.size {
			t.Fatalf("scan in pieces: %d records, hole %d, sealed %v, size %d; of the whole file: %d, %d, %v, %d",
				len(got.recs), got.hole, got.sealed, got.size, len(want.recs), want.hole, want.sealed, want.size)
		}
		if got.cp != want.cp || got.cpOK != want.cpOK || errText(got.layout) != errText(want.layout) {
			t.Fatalf("scan in pieces: checkpoint %+v (%v), layout %v; of the whole file: %+v (%v), %v",
				got.cp, got.cpOK, got.layout, want.cp, want.cpOK, want.layout)
		}
		if !bytes.Equal(got.data, b[:len(got.data)]) || (got.hole >= 0 && len(got.data) != len(b)) {
			t.Fatalf("%d bytes read, of %d, that are not the file's, or not all of it for a hole at %d", len(got.data), len(b), got.hole)
		}
		if got.hole >= 0 && len(got.recs) > 0 {
			// The classification of the hole never panics and never truncates
			// past it.
			if trim, _ := classifyTail(got.data, got.recs, got.hole); trim > got.hole {
				t.Fatalf("truncation at %d past the hole at %d", trim, got.hole)
			}
		}
		if got.sealed {
			seal := got.recs[len(got.recs)-1]
			if end := seal.off + int64(seal.length) + sb.chunk; int64(len(got.data)) >= end {
				t.Fatalf("a segment sealed at %d read %d bytes, a chunk of %d past it", seal.off, len(got.data), sb.chunk)
			}
		}
	})
}

// scanWhole is the scan of a segment's bytes b over the whole file read at
// once: the reference FuzzScanSegment holds the scan in pieces to.
func scanWhole(b []byte, n uint64) (*scanned, error) {
	s := &scanned{n: n, size: int64(len(b)), seqBreak: -1, hole: -1, trimmed: -1}
	var off int64
	for off < int64(len(b)) {
		r, ok := parseRecord(b[off:])
		if !ok {
			if err := unknownVersionAt(b, n, off); err != nil {
				return nil, err
			}
			s.hole = off
			break
		}
		s.recs = s.note(s.recs, &r, off)
		off += int64(r.length)
		if r.kind == recSeal {
			s.sealed = true
			break
		}
	}
	return s, nil
}

// errText is err's text, "" for nil: two verdicts compare by it.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// scanSeed is a segment b, the segment read before it into the same buffer,
// and the read-ahead.
type scanSeed struct {
	prev, b []byte
	chunk   uint16
}

// scanSeeds are the shapes recovery judges: a sealed segment read into a
// buffer whose stale bytes are valid records, a last segment's write
// position, a hole the stale bytes would hide a newer build's record
// behind, such a record in the file, a torn record at the file's end, a
// length out of range, an empty file and a record of an unknown kind.
func scanSeeds() []scanSeed {
	const size = 4096
	cp := &record{kind: recCheckpt, batch: 1, end: true, data: checkpoint{nextBatch: 1}.encode()}
	entry := func(i, batch uint64, end bool) *record {
		return &record{kind: recEntry, batch: batch, index: i, end: end, data: bytes.Repeat([]byte{byte(i)}, 40)}
	}
	seg := func(recs ...*record) []byte {
		var b []byte
		for _, r := range recs {
			b = appendRecord(b, r)
		}
		return b
	}
	pad := func(b []byte) []byte { return append(b, make([]byte, max(0, size-len(b)))...) }
	full := []*record{cp}
	for i := uint64(1); i <= 40; i++ {
		full = append(full, entry(i, 1+(i+1)/2, i%2 == 0))
	}
	stale := pad(seg(full...))
	head := seg(cp, entry(1, 2, false), entry(2, 2, true))
	sealed := pad(append(bytes.Clone(head), seg(&record{kind: recSeal, end: true, batch: 3, index: uint64(len(head))})...))
	newer := seg(entry(3, 3, true))
	newer[recHeader] = recVersion + 1
	sealRecord(newer)
	behind := pad(append(append(bytes.Clone(head), make([]byte, 64)...), newer...))
	short := seg(cp, entry(1, 2, true))
	unknown := seg(cp, &record{kind: 99, batch: 2, end: true})
	return []scanSeed{
		{stale, sealed, 64},
		{stale, sealed, 1},
		{stale, pad(bytes.Clone(head)), 7},
		{behind, pad(bytes.Clone(head)), 16},
		{stale, behind, 100},
		{stale, short[:len(short)-5], 9},
		{stale, pad(append(bytes.Clone(head), 0xff, 0xff, 0xff, 0x7f)), 32},
		{stale, nil, 8},
		{stale, pad(unknown), 4096},
		{nil, stale, 3},
	}
}
