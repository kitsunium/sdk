//go:build !race

package consensus

import (
	"bytes"
	"testing"
)

// recordCodecRuns is how many calls each record codec gate counts.
const recordCodecRuns int = 30_000

// The log record codec's two allocation claims, D15's "encode / decode of a
// log record: 0 allocs/op" (issue #921): appending a record into a buffer
// that already holds its capacity, as writeBatch's batch buffer does once
// warm, and parsing one, whose payload aliases the bytes read, allocate
// nothing. appendRecord and parseRecord are unexported, so neither can be a
// design budget (ADR 0165 §6 keeps the budgets on unexported paths
// hand-written); the frame codec's are, in wire's gen_perf_test.go.
//
// Mutation-checked: appendRecord copying r.data through a fresh slice
// (append(dst, bytes.Clone(r.data)...)) fails the first gate with 30 000
// allocations, and parseRecord cloning its payload (data:
// bytes.Clone(body[recFixed:])) fails the second with 30 000.
func TestTheRecordCodecAllocatesNothing(t *testing.T) {
	rec := record{
		kind: recEntry, end: true, batch: 7, term: 3, index: 101, ekind: 1,
		ever: 1, body: 0xdeadbeef, chain: 0xfeedface, data: bytes.Repeat([]byte{'x'}, 256),
	}
	buf := appendRecord(nil, &rec)
	enc := mallocsOver(recordCodecRuns, func() { buf = appendRecord(buf[:0], &rec) })
	reportWindow(t, "appendRecord", enc)
	if enc.own != 0 {
		t.Errorf("%d appendRecord calls into a warm buffer allocated %d times, want 0", recordCodecRuns, enc.own)
	}
	var sink record
	dec := mallocsOver(recordCodecRuns, func() {
		got, ok := parseRecord(buf)
		if !ok {
			t.Fatal("parseRecord refused the record appendRecord wrote")
		}
		sink = got
	})
	reportWindow(t, "parseRecord", dec)
	if dec.own != 0 {
		t.Errorf("%d parseRecord calls allocated %d times, want 0", recordCodecRuns, dec.own)
	}
	if sink.index != rec.index || !bytes.Equal(sink.data, rec.data) {
		t.Fatalf("parseRecord read %+v back, want %+v", sink, rec)
	}
}
