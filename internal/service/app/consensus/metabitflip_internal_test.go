package consensus

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// One bit flipped in Position.Index of a follower's newest .meta, after its
// sync: the .meta carries no checksum, so the follower restored at that
// index, erased its log, reported entries it never applied, and the leader
// took its acknowledgement past the leader's own log as match and committed
// writes held by its disk alone. The metadata whose id does not name its
// position is now set aside as unreadable, the log is kept, and no write
// commits without a majority (#546).
func TestAMetadataWhosePositionRottedIsNotRestored(t *testing.T) {
	root := t.TempDir()
	var mu sync.Mutex
	stores := map[coreconsensus.NodeID]*fileSnapshots{}
	dirs := map[coreconsensus.NodeID]string{}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{7, 7, 7}, size: 3, every: 20}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		s, ok := stores[tn.id]
		if !ok {
			dirs[tn.id] = filepath.Join(root, fmt.Sprint(tn.id))
			var err error
			if s, err = newFileSnapshots(dirs[tn.id], storageFailed); err != nil {
				t.Fatal(err)
			}
			stores[tn.id] = s
		}
		cfg.Snapshots = s
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	var tok coreconsensus.TokenValue
	for i := range 70 {
		tok = propose(t, l.node, fmt.Sprintf("k%d=%d", i, i))
	}
	c.waitApplied(tok.Index, 5*time.Second)
	var f, o *testNode
	for _, tn := range c.nodes {
		switch {
		case tn == l:
		case f == nil:
			f = tn
		default:
			o = tn
		}
	}
	var metas []coreconsensus.SnapshotMetaValue
	var err error
	for wait := time.Now().Add(10 * time.Second); time.Now().Before(wait); time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		metas, err = stores[f.id].List()
		mu.Unlock()
		if err == nil && len(metas) > 0 {
			break
		}
	}
	if err != nil || len(metas) == 0 {
		t.Fatalf("setup: follower snapshots %v %v", metas, err)
	}
	c.kill(f)
	_, lastBefore := f.log.Bounds()
	path := filepath.Join(dirs[f.id], metas[0].ID+".meta")
	orig := must(os.ReadFile(path))(t)
	var m coreconsensus.SnapshotMetaValue
	if err := wire.ParseSnapshotMeta(orig, &m); err != nil {
		t.Fatal(err)
	}
	m.Position.Index |= 1 << 30
	flipped := wire.AppendSnapshotMeta(nil, &m)
	diff := 0
	for i := range orig {
		for b := orig[i] ^ flipped[i]; b != 0; b &= b - 1 {
			diff++
		}
	}
	if len(flipped) != len(orig) || diff != 1 {
		t.Fatalf("setup: not a single-bit flip (%d bits)", diff)
	}
	if err := os.WriteFile(path, flipped, 0o600); err != nil {
		t.Fatal(err)
	}
	c.start(f)
	if _, last := f.log.Bounds(); last < lastBefore {
		t.Fatalf("the follower's log was cut to %d (held %d)", last, lastBefore)
	}
	if st := f.node.Status(); st.Applied > lastBefore || st.Commit > lastBefore {
		t.Fatalf("the follower reports past its log: %+v (log ends at %d)", st, lastBefore)
	}
	// The third voter is cut off: no write may commit on the leader and f
	// unless f holds it.
	c.net.Isolate(o.id, true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	w, _, perr := l.node.Propose(ctx, []byte("acked=1"))
	cancel()
	if perr == nil {
		if ents, _ := f.log.Entries(w.Index, w.Index+1, 1<<20, nil); len(ents) != 1 {
			t.Fatalf("write %d committed while held by the leader alone", w.Index)
		}
	}
}
