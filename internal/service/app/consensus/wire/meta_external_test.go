package wire_test

import (
	"reflect"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// A meta of a cluster below protocol 2 is written in version 1, which the
// release before writes; one of a cluster at 2 in version 2, which the
// release before reads; one of a cluster at 3 in version 3, which carries
// the retired keys of its configuration (#648). Each is read back as it
// was written (#546 phase 2, D18).
func TestTheMetaVersionFollowsTheClusterVersion(t *testing.T) {
	for _, tc := range []struct {
		cluster uint32
		version byte
	}{{0, 1}, {1, 1}, {2, 2}, {3, 3}} {
		m := metaForTest()
		m.ClusterVersion = tc.cluster
		if tc.cluster >= 3 {
			m.Membership.RetiredKeys = [][32]byte{{0x99}, {0x9a}}
		}
		b := wire.AppendSnapshotMeta(nil, m)
		if b[0] != tc.version {
			t.Fatalf("cluster version %d: meta written in version %d, want %d", tc.cluster, b[0], tc.version)
		}
		if tc.version == 2 && !reflect.DeepEqual(b, wire.AppendSnapshotMetaV2ForTest(nil, m)) {
			t.Fatalf("cluster version %d: the version 2 written is not the one the release before reads", tc.cluster)
		}
		var out coreconsensus.SnapshotMetaValue
		if err := wire.ParseSnapshotMeta(b, &out); err != nil {
			t.Fatalf("cluster version %d: %v", tc.cluster, err)
		}
		if !reflect.DeepEqual(out, *m) {
			t.Fatalf("cluster version %d: read back %+v, want %+v", tc.cluster, out, *m)
		}
	}
}
