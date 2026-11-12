package wire_test

import (
	"reflect"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// A metadata of a cluster at protocol 4 records the client sessions and
// reads them back, its trailing sum guarding them; one of a cluster at 3
// carries none (#918).
func TestASnapshotMetaOfVersion4CarriesTheSessions(t *testing.T) {
	m := metaForTest()
	m.ClusterVersion, m.Membership.RetiredKeys = 4, retiredKeysForTest
	m.Sessions = []coreconsensus.SessionValue{
		{ID: coreconsensus.SessionID{1}, Seq: 3, BodySum: 0xfeed, Position: coreconsensus.PositionValue{Term: 2, Index: 40}, Last: 41},
		{ID: coreconsensus.SessionID{2}, Position: coreconsensus.PositionValue{Term: 2, Index: 7}, Last: 7},
	}
	b := wire.AppendSnapshotMeta(nil, m)
	if b[0] != 4 {
		t.Fatalf("a metadata at cluster version 4 written in version %d", b[0])
	}
	var out coreconsensus.SnapshotMetaValue
	if err := wire.ParseSnapshotMeta(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, *m) {
		t.Fatalf("read back %+v, want %+v", out, *m)
	}
	for i := range b[1:] {
		x := append([]byte(nil), b...)
		x[1+i] ^= 1
		if err := wire.ParseSnapshotMeta(x, &out); err == nil {
			t.Fatalf("a bit flipped at byte %d was read", 1+i)
		}
	}
	var old coreconsensus.SnapshotMetaValue
	if err := wire.ParseSnapshotMeta(wire.AppendSnapshotMetaV3ForTest(nil, m), &old); err != nil {
		t.Fatal(err)
	}
	if old.Sessions != nil {
		t.Fatalf("a version 3 metadata carried sessions: %v", old.Sessions)
	}
}

// A session command's payload reads back as it was written, and one
// shorter than its header is FRAME_INVALID.
func TestASessionPayloadReadsBack(t *testing.T) {
	s := coreconsensus.SessionID{9, 1}
	b := wire.AppendSession(nil, s, 7, []byte("k=v"))
	gs, seq, data, err := wire.ParseSession(b)
	if err != nil || gs != s || seq != 7 || string(data) != "k=v" {
		t.Fatalf("read back %x %d %q %v", gs, seq, data, err)
	}
	if _, _, _, err := wire.ParseSession(b[:23]); !errs.HasCode(err, coreconsensus.CodeFrameInvalid) {
		t.Fatalf("a short payload: %v, want FRAME_INVALID", err)
	}
}
