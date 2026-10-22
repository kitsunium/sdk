package consensus

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// newer re-seals the record r encoded with version v: its crc still holds,
// as a newer build would have written it.
func newer(r []byte, v uint8) []byte {
	r[recHeader] = v
	sealRecord(r)
	return r
}

// segmentBytes is the bytes of every segment of dir's log, by name.
func segmentBytes(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	names := must(filepath.Glob(filepath.Join(dir, "log", "seg-????????????????")))(t)
	out := make(map[string][]byte, len(names))
	for _, n := range names {
		out[filepath.Base(n)] = must(os.ReadFile(n))(t)
	}
	return out
}

// A log a newer build wrote stops the open VERSION_UNSUPPORTED and is left
// byte for byte: a record whose crc holds but whose version this build
// does not write, a record kind it does not know, or a hard state of
// another length — in a finished batch or in the unfinished last one.
// Taken for a hole, or skipped, they were zeroed as a torn tail after a
// rollback, and the node forgot the entries it acknowledged and the vote
// it cast (D18, #511).
func TestALogANewerBuildWroteStopsTheOpen(t *testing.T) {
	longHS := append(encodeHS(coreconsensus.HardState{Term: 2, Vote: 9}), make([]byte, 8)...)
	type tc struct {
		name string
		// tail is what the newer build left past the base log, given the
		// batch the base log continues with.
		tail func(b uint64) [][]byte
	}
	cases := []tc{
		{"newer version, finished batches", func(b uint64) [][]byte {
			return [][]byte{
				newer(rec(recEntry, b, 4, false), recVersion+1),
				newer(rec(recEntry, b, 5, true), recVersion+1),
				newer(appendRecord(nil, &record{
					kind: recHardState, batch: b + 1, end: true,
					data: encodeHS(coreconsensus.HardState{Term: 2, Vote: 9}),
				}), recVersion+1),
			}
		}},
		{"newer version after a hole", func(b uint64) [][]byte {
			return [][]byte{[]byte("garbage!garbage!"), newer(rec(recEntry, b+1, 5, true), recVersion+1)}
		}},
		{"longer hard state", func(b uint64) [][]byte {
			return [][]byte{appendRecord(nil, &record{kind: recHardState, batch: b, end: true, data: longHS})}
		}},
		{"longer hard state, unfinished batch", func(b uint64) [][]byte {
			return [][]byte{appendRecord(nil, &record{kind: recHardState, batch: b, data: longHS})}
		}},
		{"unknown record kind", func(b uint64) [][]byte {
			return [][]byte{rec(recCommit+1, b, 4, true)}
		}},
		{"unknown record kind, unfinished batch", func(b uint64) [][]byte {
			return [][]byte{rec(recEntry, b, 4, false), rec(recCommit+1, b, 4, false)}
		}},
	}
	runCase := func(t *testing.T, c tc) {
		dir, d := base(t)
		var at int64
		for _, r := range c.tail(d.d.log.batch) {
			tail(t, d, r, at)
			at += int64(len(r))
		}
		d.d.log.Close()
		if err := d.Close(); err != nil {
			t.Fatalf("close before reopening: %v", err)
		}
		before := segmentBytes(t, dir)
		nd, err := NewDataDirWith(dir, DataDirOptions{SegmentBytes: 1 << 16})
		if err == nil {
			defer nd.Close()
			_, last := nd.Log().Bounds()
			t.Errorf("a log a newer build wrote opened: last=%d tailsTruncated=%d", last, nd.d.log.tailsTruncated)
		} else if !errs.HasCode(err, coreconsensus.CodeVersionUnsupported) {
			t.Errorf("open = %v %v, want VERSION_UNSUPPORTED", err, errs.FieldsOf(err))
		}
		after := segmentBytes(t, dir)
		for n, b := range before {
			if !bytes.Equal(b, after[n]) {
				t.Errorf("segment %s changed by the refused open", n)
			}
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
