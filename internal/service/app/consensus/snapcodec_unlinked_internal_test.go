package consensus

import (
	"errors"
	"fmt"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
)

// unlinkedZstd stands for third-party/transform's zstd in a build that does
// not link it: this module cannot import the vendor module, so the leader's
// codec is flate's engine under the name "zstd", registered nowhere. A node
// configured with it reads its own snapshots; a node without it finds no
// "zstd" in the registry, which is exactly a build that does not link the
// vendor. The real codec's install is third-party/transform's
// TestAClusterInstallsAZstdSnapshot (#917).
type unlinkedZstd struct {
	coretransform.BoundedDecompressor
}

// Algorithm names the codec as third-party/transform registers zstd.
func (unlinkedZstd) Algorithm() coretransform.Algorithm { return "zstd" }

// A cluster whose leader cuts its snapshots with zstd offers one to a
// follower whose build does not link zstd: the follower refuses it and stops
// VERSION_UNSUPPORTED, unknown meaning stop and never skip (D18, #917). Only
// refused, it would stay out of replication, offered the same snapshot at
// every probe (#513).
func TestAFollowerThatDoesNotLinkTheLeadersCodecStops(t *testing.T) {
	flate, _ := defaultCodec()
	var lag *testNode
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{9, 1, 7}, every: 20, size: 3,
		configure: func(tn *testNode, cfg *Config) {
			if tn != lag {
				cfg.SnapshotCodec = unlinkedZstd{flate}
			}
		},
	})
	if _, ok := coretransform.Lookup("zstd"); ok {
		t.Fatal("this test binary links a codec named zstd: the follower would read it")
	}
	l := c.settledLeader(5 * time.Second)
	lag = c.otherThan(l)
	c.kill(lag)
	for i := range 200 {
		propose(t, l.node, fmt.Sprintf("k%d=%d", i, i))
	}
	waitFor(t, "the leader compacts past the lagging node", func() bool {
		first, _ := l.log.Bounds()
		return first > 10
	})
	metas := must(l.snaps.List())(t)
	if len(metas) == 0 || metas[0].Codec != "zstd" {
		t.Fatalf("the leader's snapshots: %+v, want them cut with zstd", metas)
	}
	c.start(lag)
	waitFor(t, "the lagging node stops on the leader's snapshot", func() bool {
		_, stopped := lag.node.StopReason()
		return stopped
	})
	r, _ := lag.node.StopReason()
	if !errors.Is(r.Err, coreconsensus.VersionUnsupported) {
		t.Fatalf("stop reason %+v, want VERSION_UNSUPPORTED", r)
	}
	if v, _ := lag.sm.get("k199"); v != "" {
		t.Fatalf("the node installed a snapshot of a codec it does not link: k199=%q", v)
	}
	// The two nodes that link it still commit without it.
	propose(t, l.node, "after=1")
}
