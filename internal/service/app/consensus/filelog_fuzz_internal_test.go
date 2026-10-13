package consensus

import "testing"

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
		var recs []record
		var off int
		for off < len(b) {
			r, ok := parseRecord(b[off:])
			if !ok {
				break
			}
			r.off = int64(off)
			recs = append(recs, r)
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
