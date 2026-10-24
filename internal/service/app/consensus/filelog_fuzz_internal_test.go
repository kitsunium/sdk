package consensus

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// FuzzParseRecord: the log record decoder never panics, and a record it
// accepts never claims more bytes than it was given.
func FuzzParseRecord(f *testing.F) {
	f.Add(appendRecord(nil, &record{kind: recEntry, batch: 1, term: 1, index: 1, data: []byte("x"), end: true}))
	f.Fuzz(func(t *testing.T, b []byte) {
		r, ok := parseRecord(b)
		if ok && r.length > len(b)+7 {
			t.Fatalf("record of %d bytes from %d", r.length, len(b))
		}
	})
}

// FuzzClassifyTail: the tail classification never panics and never
// truncates past the hole it was given.
func FuzzClassifyTail(f *testing.F) {
	cp := appendRecord(nil, &record{kind: recCheckpt, batch: 1, end: true, data: checkpoint{nextBatchSeq: 1}.encode()})
	e := appendRecord(nil, &record{kind: recEntry, batch: 2, term: 1, index: 1, data: []byte("x")})
	f.Add(append(append(cp, e...), make([]byte, 64)...))
	f.Fuzz(func(t *testing.T, b []byte) {
		var recs []scanRec
		var off int
		for off < len(b) {
			r, ok := parseRecord(b[off:])
			if !ok {
				break
			}
			recs = append(recs, scanRecOf(&r, int64(off)))
			off += r.length
		}
		if len(recs) == 0 || off >= len(b) {
			return
		}
		if trim, _ := classifyTail(b, recs, int64(off)); trim > int64(off) {
			t.Fatalf("truncation at %d past the hole at %d", trim, off)
		}
	})
}

// FuzzScanSegment: a segment scanned in pieces, into a buffer that held
// another segment, gives what a parse of the whole file gives: the same
// records, payloads, hole, seal and verdict, and bytes read that are the
// file's, all of them for a hole. The scan read every segment whole and
// kept them all until recover returned (#630); it now reads a sealed one up
// to its seal, and the rest of a file only for a hole.
func FuzzScanSegment(f *testing.F) {
	for _, s := range scanSeeds() {
		f.Add(s.prev, s.b, s.chunk)
	}
	f.Fuzz(func(t *testing.T, prev, b []byte, chunk uint16) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "log"), 0o700); err != nil {
			t.Fatal(err)
		}
		for i, seg := range [][]byte{prev, b} {
			if err := os.WriteFile(filepath.Join(dir, "log", segName(uint64(i+1))), seg, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		l := &fileLog{dir: dir}
		sb := scanBufs{chunk: int64(chunk%4096) + 1}
		// Whatever its verdict, the first segment leaves its bytes in the
		// buffer the second is read into.
		if _, err := l.scanSegment(1, &sb); err != nil {
			t.Logf("first segment: %v", err)
		}
		got, err := l.scanSegment(2, &sb)
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
		if len(got.pay) != len(want.pay) {
			t.Fatalf("%d payloads kept, want %d", len(got.pay), len(want.pay))
		}
		for i, p := range want.pay {
			if !bytes.Equal(got.pay[i], p) {
				t.Fatalf("payload of record %d: %x, want %x", i, got.pay[i], p)
			}
		}
		if !bytes.Equal(got.data, b[:len(got.data)]) || (got.hole >= 0 && len(got.data) != len(b)) {
			t.Fatalf("%d bytes read, of %d, that are not the file's, or not all of it for a hole at %d", len(got.data), len(b), got.hole)
		}
		if got.sealed {
			seal := got.recs[len(got.recs)-1]
			if end := seal.off + int64(seal.length) + sb.chunk; int64(len(got.data)) >= end {
				t.Fatalf("a segment sealed at %d read %d bytes, a chunk of %d past it", seal.off, len(got.data), sb.chunk)
			}
		}
	})
}

// scanWhole is the scan of a segment's bytes b as it ran before #630, over
// the whole file read at once: the reference FuzzScanSegment holds the scan
// in pieces to.
func scanWhole(b []byte, n uint64) (*scanned, error) {
	s := &scanned{n: n, size: int64(len(b)), hole: -1, trimmed: -1}
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
		if r.kind == recCheckpt || r.kind == recHardState {
			s.keep(len(s.recs), r.data)
		}
		s.recs = append(s.recs, scanRecOf(&r, off))
		off += int64(r.length)
		if r.kind == recSeal {
			s.sealed = true
			break
		}
	}
	return s, nil
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
// length out of range, an empty file and a hard state of another length.
func scanSeeds() []scanSeed {
	const size = 4096
	cp := &record{kind: recCheckpt, batch: 1, end: true, data: checkpoint{nextBatchSeq: 1}.encode()}
	entry := func(i, batch uint64, end bool) *record {
		return &record{kind: recEntry, batch: batch, term: 1, index: i, end: end, data: bytes.Repeat([]byte{byte(i)}, 40)}
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
	newer[8] = recVersion + 1
	sealRecord(newer)
	behind := pad(append(append(bytes.Clone(head), make([]byte, 64)...), newer...))
	short := seg(cp, entry(1, 2, true))
	hs := seg(cp, &record{kind: recHardState, batch: 2, end: true, data: encodeHS(coreconsensus.HardState{Term: 2, Vote: 1})[:16]})
	return []scanSeed{
		{stale, sealed, 64},
		{stale, sealed, 1},
		{stale, pad(bytes.Clone(head)), 7},
		{behind, pad(bytes.Clone(head)), 16},
		{stale, behind, 100},
		{stale, short[:len(short)-5], 9},
		{stale, pad(append(bytes.Clone(head), 0xff, 0xff, 0xff, 0x7f)), 32},
		{stale, nil, 8},
		{stale, pad(hs), 4096},
		{nil, stale, 3},
	}
}
