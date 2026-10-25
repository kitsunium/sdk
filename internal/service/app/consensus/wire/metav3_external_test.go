package wire_test

import (
	"errors"
	"reflect"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// retiredKeysForTest are the keys of two members removed at protocol 3.
var retiredKeysForTest = [][32]byte{{0x99, 1}, {0x9a, 2}}

// A metadata of version 3 carries the retired keys of its configuration
// and reads them back; versions 1 and 2, which a cluster below protocol 3
// writes, carry none (#648).
func TestASnapshotMetaOfVersion3CarriesTheRetiredKeys(t *testing.T) {
	m := metaForTest()
	m.ClusterVersion, m.Membership.Retired, m.Membership.RetiredKeys = 3, []coreconsensus.NodeID{5, 6}, retiredKeysForTest
	var out coreconsensus.SnapshotMetaValue
	if err := wire.ParseSnapshotMeta(wire.AppendSnapshotMetaV3ForTest(nil, m), &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, *m) {
		t.Fatalf("read back %+v, want %+v", out, *m)
	}
	for _, v := range []func([]byte, *coreconsensus.SnapshotMetaValue) []byte{wire.AppendSnapshotMetaV1ForTest, wire.AppendSnapshotMetaV2ForTest} {
		var old coreconsensus.SnapshotMetaValue
		if err := wire.ParseSnapshotMeta(v(nil, m), &old); err != nil {
			t.Fatal(err)
		}
		if len(old.Membership.RetiredKeys) != 0 {
			t.Fatalf("a version below 3 carried retired keys: %v", old.Membership.RetiredKeys)
		}
	}
}

// Every single bit flipped in a version 3 metadata is refused, as in
// version 2: the version byte names a version this node does not read, or
// version 1 or 2, which then fail on the bytes left or the sum; any other
// bit fails the CRC-32C. A count of
// retired keys past the bytes left is refused before anything is
// allocated (#648).
func TestEveryBitFlippedInAVersion3MetaIsRefused(t *testing.T) {
	m := metaForTest()
	m.ClusterVersion, m.Membership.RetiredKeys = 3, retiredKeysForTest
	enc := wire.AppendSnapshotMetaV3ForTest(nil, m)
	for i := range enc {
		for bit := range 8 {
			b := append([]byte(nil), enc...)
			b[i] ^= 1 << bit
			var out coreconsensus.SnapshotMetaValue
			err := wire.ParseSnapshotMeta(b, &out)
			want := coreconsensus.FrameInvalid
			if i == 0 && b[0] != 1 && b[0] != 2 {
				want = coreconsensus.VersionUnsupported
			}
			if !errors.Is(err, want) {
				t.Fatalf("byte %d bit %d flipped: %v, want %v", i, bit, err, want)
			}
		}
	}
}

// An admin payload of version 3 carries Origin and the retired keys of its
// membership, which versions 0 and 2 do not; a node of protocol 2, which
// reads versions 0 and 2 only, refuses it VERSION_UNSUPPORTED, as this
// node refuses a version it does not know (D18, #648).
func TestAnAdminPayloadOfVersion3CarriesTheRetiredKeys(t *testing.T) {
	a := wire.AdminValue{
		Operator: "op", Change: wire.ChangeRemove, Target: 6, Origin: 2,
		Membership: coreconsensus.MembershipValue{Index: 9, Retired: []coreconsensus.NodeID{5, 6}, RetiredKeys: retiredKeysForTest},
	}
	got, err := wire.ParseAdminAt(wire.AdminVersion3, wire.AppendAdminAt(nil, wire.AdminVersion3, &a))
	if err != nil || !reflect.DeepEqual(got, a) {
		t.Fatalf("version 3 read back %+v (%v), want %+v", got, err, a)
	}
	for _, v := range []uint32{0, wire.AdminVersion2} {
		old, err := wire.ParseAdminAt(v, wire.AppendAdminAt(nil, v, &a))
		if err != nil || len(old.Membership.RetiredKeys) != 0 {
			t.Fatalf("version %d: retired keys %v (%v), want none", v, old.Membership.RetiredKeys, err)
		}
	}
	if _, err := wire.ParseAdminAt(wire.AdminVersion3+1, nil); !errors.Is(err, coreconsensus.VersionUnsupported) {
		t.Fatalf("an admin version this node does not know: %v", err)
	}
	b := wire.AppendAdminAt(nil, wire.AdminVersion3, &a)
	b = append(b[:len(b)-4-2*32], 0xff, 0xff, 0xff, 0x0f)
	if _, err := wire.ParseAdminAt(wire.AdminVersion3, b); !errors.Is(err, coreconsensus.FrameInvalid) {
		t.Fatalf("a retired key count past the bytes left: %v", err)
	}
}
