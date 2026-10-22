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

// reopenRefusedVersion closes d, reopens dir and requires the open to be
// refused VERSION_UNSUPPORTED with every segment byte left as it was.
func reopenRefusedVersion(t *testing.T, dir string, d DataDir) {
	t.Helper()
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

// A record whose crc holds but whose version this build does not know was
// written by a newer build: D18 stops on it, and a rollback used to zero it
// as a torn tail — entries acknowledged and a vote cast were lost (#511).
func TestALogRecordOfANewerVersionStopsTheOpen(t *testing.T) {
	dir, d := base(t)
	b := d.d.log.batch
	r1 := newer(rec(recEntry, b, 4, false), recVersion+1)
	r2 := newer(rec(recEntry, b, 5, true), recVersion+1)
	hs := newer(appendRecord(nil, &record{
		kind: recHardState, batch: b + 1, end: true,
		data: encodeHS(coreconsensus.HardState{Term: 2, Vote: 9}),
	}), recVersion+1)
	tail(t, d, r1, 0)
	tail(t, d, r2, int64(len(r1)))
	tail(t, d, hs, int64(len(r1)+len(r2)))
	reopenRefusedVersion(t, dir, d)
}

// A newer record after a real hole is no torn tail either.
func TestALogRecordOfANewerVersionAfterAHoleStopsTheOpen(t *testing.T) {
	dir, d := base(t)
	b := d.d.log.batch
	garbage := []byte("garbage!garbage!")
	r1 := newer(rec(recEntry, b+1, 5, true), recVersion+1)
	tail(t, d, garbage, 0)
	tail(t, d, r1, int64(len(garbage)))
	reopenRefusedVersion(t, dir, d)
}

// A hard state of another length is a newer layout, never ignored (#511).
func TestALongerHardStateStopsTheOpen(t *testing.T) {
	dir, d := base(t)
	b := d.d.log.batch
	data := append(encodeHS(coreconsensus.HardState{Term: 2, Vote: 9}), make([]byte, 8)...)
	tail(t, d, appendRecord(nil, &record{kind: recHardState, batch: b, end: true, data: data}), 0)
	reopenRefusedVersion(t, dir, d)
}

// A record kind this build does not know is never skipped (#511).
func TestAnUnknownRecordKindStopsTheOpen(t *testing.T) {
	dir, d := base(t)
	b := d.d.log.batch
	tail(t, d, rec(recCommit+1, b, 4, true), 0)
	reopenRefusedVersion(t, dir, d)
}
