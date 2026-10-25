package wire_test

import (
	"errors"
	"reflect"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// metaForTest is a metadata with every variable-length field filled.
func metaForTest() *coreconsensus.SnapshotMetaValue {
	return &coreconsensus.SnapshotMetaValue{
		ID: "00000000000000000020-00000000000000000002", Version: 1,
		Position: coreconsensus.PositionValue{Term: 2, Index: 20},
		Membership: coreconsensus.MembershipValue{Index: 3, Members: []coreconsensus.MemberValue{
			{ID: 1, Slot: "a", Address: "127.0.0.1:1", Voter: true, PayloadMax: 1, MaxSnapshotBytes: 1 << 20},
		}},
		ChainTip: 7, Codec: "flate", EncodedSize: 10, DecodedSize: 20, ChunkSize: 20,
		SHA256: [32]byte{1, 2, 3}, ClusterVersion: 1, ClusterGeneration: 1, Reserved: []byte{9},
	}
}

// A metadata of version 2 — the fields of version 1, then their CRC-32C —
// is read by this release, which still writes version 1: a node rolled back
// to it, or running beside the release that writes version 2, reads every
// .meta and offer of it instead of skipping it as unknown (#546 phase 1,
// ADR 0152 D18).
func TestASnapshotMetaOfVersion2IsRead(t *testing.T) {
	m := metaForTest()
	var v1, v2 coreconsensus.SnapshotMetaValue
	if err := wire.ParseSnapshotMeta(wire.AppendSnapshotMeta(nil, m), &v1); err != nil {
		t.Fatal(err)
	}
	if err := wire.ParseSnapshotMeta(wire.AppendSnapshotMetaV2ForTest(nil, m), &v2); err != nil {
		t.Fatalf("version 2 refused: %v", err)
	}
	if !reflect.DeepEqual(v1, *m) || !reflect.DeepEqual(v2, *m) {
		t.Fatalf("decoded\nv1 %+v\nv2 %+v\nwant %+v", v1, v2, *m)
	}
	if b := wire.AppendSnapshotMeta(nil, &v2); b[0] != 1 {
		t.Fatalf("this release wrote version %d", b[0])
	}
}

// Every single bit flipped in a version 2 metadata is refused: the version
// byte names a version this node does not read, or version 3, whose sum
// then fails; any other bit fails the CRC-32C, FRAME_INVALID. Version 1
// carries no sum, which is why its reader cross-checks the id against the
// position (#546).
func TestEveryBitFlippedInAVersion2MetaIsRefused(t *testing.T) {
	enc := wire.AppendSnapshotMetaV2ForTest(nil, metaForTest())
	for i := range enc {
		for bit := range 8 {
			b := append([]byte(nil), enc...)
			b[i] ^= 1 << bit
			var out coreconsensus.SnapshotMetaValue
			err := wire.ParseSnapshotMeta(b, &out)
			want := coreconsensus.FrameInvalid
			if i == 0 && b[0] != 3 {
				want = coreconsensus.VersionUnsupported
			}
			if !errors.Is(err, want) {
				t.Fatalf("byte %d bit %d flipped: %v, want %v", i, bit, err, want)
			}
		}
	}
	var out coreconsensus.SnapshotMetaValue
	for _, b := range [][]byte{nil, {2}, {2, 0, 0, 0}, enc[:len(enc)-1]} {
		if err := wire.ParseSnapshotMeta(b, &out); !errors.Is(err, coreconsensus.FrameInvalid) {
			t.Fatalf("truncated %x: %v", b, err)
		}
	}
}
