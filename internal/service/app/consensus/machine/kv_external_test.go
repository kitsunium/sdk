package machine_test

import (
	"bytes"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/machine"
)

func apply(s *machine.KV, cmd []byte) any {
	return s.Apply(coreconsensus.EntryValue{Kind: coreconsensus.EntryCommand, Data: cmd})
}

func TestKVPutGetAndCompareAndSwap(t *testing.T) {
	s := machine.NewKV()
	if r := apply(s, machine.KVGet("k")).(machine.KVResult); r.Found {
		t.Fatalf("an empty KV found k: %+v", r)
	}
	if r := apply(s, machine.KVCompareAndSwap("k", []byte("v1"), 0)).(machine.KVResult); !r.Applied || r.Revision != 1 {
		t.Fatalf("a CAS at revision 0 on an absent key = %+v, want applied at revision 1", r)
	}
	if r := apply(s, machine.KVPut("k", []byte("v2"))).(machine.KVResult); !r.Applied || r.Revision != 2 {
		t.Fatalf("a put = %+v, want revision 2", r)
	}
	// The ABA defence: the value is back to an earlier one, the revision is not.
	if r := apply(s, machine.KVCompareAndSwap("k", []byte("v1"), 1)).(machine.KVResult); r.Applied || r.Revision != 2 || string(r.Value) != "v2" {
		t.Fatalf("a CAS on a stale revision = %+v, want refused with the current one", r)
	}
	if r := apply(s, machine.KVGet("k")).(machine.KVResult); !r.Found || string(r.Value) != "v2" || r.Revision != 2 {
		t.Fatalf("a get = %+v", r)
	}
	if v, rev, ok := s.Get("k"); !ok || string(v) != "v2" || rev != 2 {
		t.Fatalf("Get = %q %d %v", v, rev, ok)
	}
}

func TestKVRefusesACommandItDoesNotRead(t *testing.T) {
	s := machine.NewKV()
	for _, cmd := range [][]byte{nil, {9}, {2, 200}} {
		if err, _ := apply(s, cmd).(error); !errs.HasCode(err, coreconsensus.CodeVersionUnsupported) {
			t.Errorf("command %v = %v, want VERSION_UNSUPPORTED", cmd, err)
		}
	}
}

func TestKVSnapshotRestoresTheStateItCopied(t *testing.T) {
	s := machine.NewKV()
	apply(s, machine.KVPut("a", []byte("1")))
	apply(s, machine.KVPut("b", nil))
	apply(s, machine.KVPut("a", []byte("2")))
	snap, _ := s.Snapshot()
	apply(s, machine.KVPut("c", []byte("after")))
	var buf bytes.Buffer
	if _, err := snap.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	snap.Release()
	r := machine.NewKV()
	if err := r.Restore(bytes.NewReader(buf.Bytes()), coreconsensus.SnapshotMetaValue{}); err != nil {
		t.Fatalf("Restore() = %v", err)
	}
	if v, rev, _ := r.Get("a"); string(v) != "2" || rev != 2 {
		t.Errorf("a = %q at %d, want 2 at revision 2", v, rev)
	}
	if _, rev, ok := r.Get("b"); !ok || rev != 1 {
		t.Errorf("b = %d %v, want revision 1", rev, ok)
	}
	if _, _, ok := r.Get("c"); ok {
		t.Error("the snapshot holds a write made after it was taken")
	}
	newer := append([]byte("KKV\x02"), buf.Bytes()[4:]...)
	if err := r.Restore(bytes.NewReader(newer), coreconsensus.SnapshotMetaValue{}); !errs.HasCode(err, coreconsensus.CodeVersionUnsupported) {
		t.Errorf("a newer format = %v, want VERSION_UNSUPPORTED", err)
	}
	if err := r.Restore(bytes.NewReader(buf.Bytes()[:buf.Len()-1]), coreconsensus.SnapshotMetaValue{}); !errs.HasCode(err, coreconsensus.CodeSnapshotCorrupt) {
		t.Errorf("a cut snapshot = %v, want SNAPSHOT_CORRUPT", err)
	}
}
