package consensus

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// raiseToProtocol2 raises the cluster to protocol version 2 through the
// leader, once every member was heard announcing it, and waits until every
// node applied the raise.
func raiseToProtocol2(t *testing.T, c *cluster) {
	t.Helper()
	var nodes []*Node
	for _, tn := range c.nodes {
		if tn.node != nil {
			nodes = append(nodes, tn.node)
		}
	}
	raiseNodes(t, nodes)
}

// raiseNodes raises the cluster of nodes to protocol version 2 through
// whichever of them leads, and waits until every one applied the raise.
func raiseNodes(t *testing.T, nodes []*Node) {
	t.Helper()
	raiseNodesTo(t, nodes, raft.Protocol2)
}

// raiseNodesTo raises the cluster of nodes to protocol version v through
// whichever of them leads, and waits until every one applied the raise.
func raiseNodesTo(t *testing.T, nodes []*Node, v uint32) {
	t.Helper()
	var err error
	for wait := time.Now().Add(5 * time.Second); time.Now().Before(wait); time.Sleep(20 * time.Millisecond) {
		i := slices.IndexFunc(nodes, func(n *Node) bool { return n.Status().Role == coreconsensus.RoleLeader })
		if i < 0 {
			continue
		}
		st := nodes[i].Status()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = nodes[i].RaiseClusterVersion(ctx, "op:raise", coreconsensus.PreconditionSpec{Log: st.Log, Generation: st.Versions.ClusterGeneration}, v)
		cancel()
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("raise to protocol %d: %v", v, err)
	}
	for wait := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if !slices.ContainsFunc(nodes, func(n *Node) bool { return n.Status().Versions.Cluster != v }) {
			return
		}
		if time.Now().After(wait) {
			t.Fatalf("the raise to protocol %d did not reach every node", v)
		}
	}
}

// fileSnapshotCluster is a three-node cluster whose nodes keep their
// snapshots in a fileSnapshots under root, kept across restarts.
func fileSnapshotCluster(t *testing.T) (c *cluster, stores func(coreconsensus.NodeID) (*fileSnapshots, string)) {
	root := t.TempDir()
	var mu sync.Mutex
	byID := map[coreconsensus.NodeID]*fileSnapshots{}
	dirs := map[coreconsensus.NodeID]string{}
	c = &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{7, 7, 8}, size: 3, every: 20}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		s, ok := byID[tn.id]
		if !ok {
			dirs[tn.id] = filepath.Join(root, fmt.Sprint(tn.id))
			var err error
			if s, err = newFileSnapshots(dirs[tn.id], storageFailed); err != nil {
				t.Fatal(err)
			}
			byID[tn.id] = s
		}
		cfg.Snapshots = s
	}
	newClusterWith(t, c)
	return c, func(id coreconsensus.NodeID) (*fileSnapshots, string) {
		mu.Lock()
		defer mu.Unlock()
		return byID[id], dirs[id]
	}
}

// One bit of a follower's newest snapshot meta flips after the sync, in
// the chain tip, which the meta's id does not name: phase 1 (S1) could not
// see it. Once the cluster speaks protocol 2 the meta is written in
// version 2, checksummed, so the rotted meta is set aside like a corrupt
// snapshot and its log is kept (#546, phase 2).
func TestAVersion2MetaThatRottedIsNotRestored(t *testing.T) {
	c, stores := fileSnapshotCluster(t)
	raiseToProtocol2(t, c)
	l := c.settledLeader(5 * time.Second)
	var tok coreconsensus.TokenValue
	for i := range 70 {
		tok = propose(t, l.node, fmt.Sprintf("k%d=%d", i, i))
	}
	c.waitApplied(tok.Index, 5*time.Second)
	f := c.otherThan(l)
	s, dir := stores(f.id)
	var metas []coreconsensus.SnapshotMetaValue
	var err error
	for wait := time.Now().Add(10 * time.Second); time.Now().Before(wait); time.Sleep(10 * time.Millisecond) {
		metas, err = s.List()
		if err == nil && len(metas) > 0 && metas[0].ClusterVersion == raft.Protocol2 {
			break
		}
	}
	if err != nil || len(metas) == 0 || metas[0].ClusterVersion != raft.Protocol2 {
		t.Fatalf("setup: no snapshot cut at protocol 2: %v %v", metas, err)
	}
	c.kill(f)
	_, lastBefore := f.log.Bounds()
	path := filepath.Join(dir, metas[0].ID+".meta")
	orig := must(os.ReadFile(path))(t)
	if orig[0] != 2 {
		t.Fatalf("a meta of a cluster at protocol 2 written in version %d", orig[0])
	}
	// The byte the chain tip's lowest bit lives in: the encoding of the
	// meta with that bit flipped differs from the original there alone,
	// checksum aside.
	rotted := metas[0]
	rotted.ChainTip ^= 1
	other := wire.AppendSnapshotMeta(nil, &rotted)
	at := -1
	for i := range len(orig) - 4 {
		if orig[i] != other[i] {
			at = i
		}
	}
	if at < 0 {
		t.Fatal("setup: the chain tip is not in the meta")
	}
	flipped := slices.Clone(orig)
	flipped[at] ^= orig[at] ^ other[at]
	if err := os.WriteFile(path, flipped, 0o600); err != nil {
		t.Fatal(err)
	}
	c.start(f)
	listed, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range listed {
		// Restarted on the snapshot before it, the follower replays its
		// log to the same commit and may cut again at the same index and
		// term, under the same id: a fresh meta written from its own state,
		// over the rotted one (#753). Listed under that id, only the
		// original's content may stand, never the rotted chain tip.
		if m.ID == metas[0].ID && m.ChainTip != metas[0].ChainTip {
			t.Fatalf("the rotted meta %s is listed with chain tip %d (original %d, rotted %d)", m.ID, m.ChainTip, metas[0].ChainTip, rotted.ChainTip)
		}
	}
	if _, last := f.log.Bounds(); last < lastBefore {
		t.Fatalf("the follower's log was cut to %d (held %d)", last, lastBefore)
	}
	tok = propose(t, l.node, "after=1")
	c.waitApplied(tok.Index, 5*time.Second)
}

// attrLogger keeps every Warn line with its attributes.
type attrLogger struct {
	mu    sync.Mutex
	lines []map[string]string
}

func (l *attrLogger) Log(_ context.Context, lv level.Level, msg string, attrs ...corelogger.AttrValue) {
	if lv != level.Warn {
		return
	}
	line := map[string]string{"msg": msg}
	for _, a := range attrs {
		if a.Value.Kind() == corelogger.KindUint64 {
			line[a.Key] = fmt.Sprint(a.Value.Uint64())
		} else {
			line[a.Key] = a.Value.String()
		}
	}
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
}
func (l *attrLogger) With(...corelogger.AttrValue) corelogger.Logger { return l }
func (l *attrLogger) WithGroup(string) corelogger.Logger             { return l }
func (l *attrLogger) Enabled(context.Context, level.Level) bool      { return true }

// A Remove submitted on a follower with an operator of the caller's choice
// is committed, once the cluster speaks protocol 2, with the follower's
// authenticated id as its origin, and the leader logs the entry it appended
// naming that follower: the leader logged nothing, and no field of the
// entry named the node that sent it (ADR 0152 D8, #561).
func TestAForwardedRemoveRecordsTheFollowerAsItsOrigin(t *testing.T) {
	logs := map[coreconsensus.NodeID]*attrLogger{}
	var mu sync.Mutex
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{7, 7, 9}, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		if logs[tn.id] == nil {
			logs[tn.id] = &attrLogger{}
		}
		cfg.Logger = logs[tn.id]
	}
	newClusterWith(t, c)
	raiseToProtocol2(t, c)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	var victim *testNode
	for _, tn := range c.nodes {
		if tn != l && tn != f {
			victim = tn
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.node.Remove(ctx, "forgejo-user:1", victim.id); err != nil {
		t.Fatalf("remove through the follower: %v", err)
	}
	_, last := l.log.Bounds()
	es := must(l.log.Entries(1, last+1, 1<<30, nil))(t)
	var found bool
	for _, e := range es {
		if e.Kind != coreconsensus.EntryMembership {
			continue
		}
		a, err := wire.ParseAdminAt(e.Version, e.Data)
		if err != nil || a.Change != wire.ChangeRemove {
			continue
		}
		found = true
		if e.Version != wire.AdminVersion2 || a.Origin != f.id || a.Operator != "forgejo-user:1" {
			t.Fatalf("the Remove f forwarded: version %d origin %d operator %q, want version %d origin %d", e.Version, a.Origin, a.Operator, wire.AdminVersion2, f.id)
		}
	}
	if !found {
		t.Fatal("setup: no Remove in the leader's log")
	}
	lg := logs[l.id]
	lg.mu.Lock()
	defer lg.mu.Unlock()
	for _, line := range lg.lines {
		if line["msg"] == "consensus: administrative entry appended" && line["origin"] == fmt.Sprint(uint64(f.id)) && line["change"] == fmt.Sprint(wire.ChangeRemove) {
			return
		}
	}
	t.Fatalf("the leader logged no administrative entry naming %d: %v", f.id, lg.lines)
}
