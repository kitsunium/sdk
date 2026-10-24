package consensus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// A node whose snapshots name a cluster version above the protocol this
// build speaks refuses to start VERSION_UNSUPPORTED, before it restores a
// state it may not read: once the cluster raised past it, cut a snapshot
// and compacted its log past the raise entry, the raise reaches a node
// rolled back only as the snapshot's ClusterVersion. It started, reported
// that version and kept applying; only the raise replayed from the log
// stopped a node (#640, D18 "unknown means stop").
func TestANodeRefusesToStartOnASnapshotAboveItsProtocol(t *testing.T) {
	c, stores := fileSnapshotCluster(t)
	l := c.settledLeader(5 * time.Second)
	var tok coreconsensus.TokenValue
	for i := range 70 {
		tok = propose(t, l.node, fmt.Sprintf("k%d=%d", i, i))
	}
	c.waitApplied(tok.Index, 5*time.Second)
	f := c.otherThan(l)
	s, dir := stores(f.id)
	waitFor(t, "the follower holds a snapshot", func() bool {
		metas, err := s.List()
		return err == nil && len(metas) > 0
	})
	c.kill(f)
	// Listed again once the follower stopped: a cut may have been published
	// between the wait and the stop.
	metas, err := s.List()
	if err != nil || len(metas) == 0 {
		t.Fatalf("setup: %v %v", metas, err)
	}
	for _, m := range metas {
		m.ClusterVersion, m.ClusterGeneration = raft.ProtocolMax+1, 1
		if err := os.WriteFile(filepath.Join(dir, m.ID+".meta"), wire.AppendSnapshotMeta(nil, &m), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.sm = newKV()
	n, err := NewNode(c.config(f))
	if err == nil {
		f.node = n
		t.Fatalf("a node of ProtocolMax %d started on snapshots at cluster version %d (versions %+v)",
			raft.ProtocolMax, raft.ProtocolMax+1, n.Status().Versions)
	}
	if !errors.Is(err, coreconsensus.VersionUnsupported) {
		t.Fatalf("a start on snapshots above the protocol: %v, want VERSION_UNSUPPORTED", err)
	}
}

// An offer whose snapshot names a cluster version above the protocol this
// build speaks stops the node, after refusing it, as one of an unknown
// metadata version does (#640, #513): installed, the node ran at a cluster
// version whose formats it cannot read.
func TestAnOfferOfASnapshotAboveTheProtocolStopsTheNode(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	waitFor(t, "the follower knows the leader", func() bool {
		return f.node.Status().Leader == l.id
	})
	st := f.node.Status()
	id := fmt.Sprintf("%020d-%020d", st.Commit+100, st.Term)
	acc := f.node.offer(coreconsensus.AuthenticatedPeerValue{ID: l.id}, coreconsensus.InstallOfferValue{
		Term: st.Term, SnapshotID: id,
		Meta: coreconsensus.SnapshotMetaValue{
			ID: id, Version: 1, Log: f.node.lineage(), ClusterVersion: raft.ProtocolMax + 1, ClusterGeneration: 1,
			Position: coreconsensus.PositionValue{Term: st.Term, Index: st.Commit + 100},
		},
	})
	if acc.Refused != coreconsensus.CodeVersionUnsupported {
		t.Fatalf("offer of a snapshot at cluster version %d: %+v", raft.ProtocolMax+1, acc)
	}
	if r, ok := f.node.StopReason(); !ok || !errors.Is(r.Err, coreconsensus.VersionUnsupported) {
		t.Fatalf("stop reason: %+v %v", r, ok)
	}
}

// The offline readers refuse VERSION_UNSUPPORTED a snapshot whose expected
// metadata names a cluster version above the protocol this build speaks,
// before any byte is read or written: RestoreSnapshot and ImportObserved
// founded a new history at that version, and OpenSnapshot read a state in
// formats this build may not know (#640).
func TestTheOfflineReadersRefuseASnapshotAboveTheProtocol(t *testing.T) {
	c := newCluster(t, 1, 0)
	tn := c.nodes[0]
	c.leader(5 * time.Second)
	for range 10 {
		propose(t, tn.node, "k=v")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var buf bytes.Buffer
	meta, err := tn.node.SaveSnapshot(ctx, "op:save", &buf)
	if err != nil {
		t.Fatal(err)
	}
	above := meta
	above.ClusterVersion = raft.ProtocolMax + 1
	if _, err := OpenSnapshot(bytes.NewReader(buf.Bytes()), &RestoreOptions{Expected: above, MaxDecoded: 1 << 30}); !errors.Is(err, coreconsensus.VersionUnsupported) {
		t.Fatalf("OpenSnapshot above the protocol: %v", err)
	}
	dir := t.TempDir()
	d := openDir(t, dir, 16<<10)
	defer d.Close()
	next := coreconsensus.ClusterID{6, 4, 0}
	id, inc, err := Init(d, next, "r")
	if err != nil {
		t.Fatal(err)
	}
	members := []coreconsensus.MemberValue{{ID: id, Incarnation: inc, Slot: "r", SPKI: MemorySPKI(id), Voter: true}}
	opt := RestoreOptions{Expected: above, Confirm: next.String(), Operator: "op:restore", MaxEncoded: 1 << 30, MaxDecoded: 1 << 30}
	if _, err := RestoreSnapshot(d, bytes.NewReader(buf.Bytes()), next, members, &opt); !errors.Is(err, coreconsensus.VersionUnsupported) {
		t.Fatalf("RestoreSnapshot above the protocol: %v", err)
	}
	tail := []coreconsensus.EntryValue{{Position: coreconsensus.PositionValue{Term: meta.Position.Term, Index: meta.Position.Index + 1}, Kind: coreconsensus.EntryNoOp}}
	if _, err := ImportObserved(d, &ImportParams{Snapshot: bytes.NewReader(buf.Bytes()), Tail: tail, Machine: newKV()}, next, members, &opt); !errors.Is(err, coreconsensus.VersionUnsupported) {
		t.Fatalf("ImportObserved above the protocol: %v", err)
	}
	r, err := Inspect(d)
	if err != nil {
		t.Fatal(err)
	}
	if r.Stamp.State != coreconsensus.StampJoining || r.Last >= r.First || len(r.Snapshots) != 0 {
		t.Fatalf("a refused restore wrote the directory: stamp %v, log [%d, %d], %d snapshot(s)", r.Stamp.State, r.First, r.Last, len(r.Snapshots))
	}
}

// A Recover whose interrupted install published a snapshot above the
// protocol this build speaks is refused VERSION_UNSUPPORTED before it writes
// anything, as a start on that snapshot is (#640). interruptedInstall read
// the refusal as a snapshot that fails its checksums and skipped it: Recover
// planned from the short log below it and wrote a new history from a state
// older than the one the directory holds.
func TestRecoverRefusesAnInterruptedInstallAboveTheProtocol(t *testing.T) {
	c, nodes, keep := installInterrupted(t)
	m := must(keep.dd.Snapshots().List())(t)[0]
	if err := keep.dd.Close(); err != nil {
		t.Fatal(err)
	}
	m.ClusterVersion, m.ClusterGeneration = raft.ProtocolMax+1, 1
	if err := os.WriteFile(filepath.Join(keep.dir, "snap", m.ID+".meta"), wire.AppendSnapshotMeta(nil, &m), 0o600); err != nil {
		t.Fatal(err)
	}
	keep.dd = openDir(t, keep.dir, 16<<10)
	defer func() {
		if err := keep.dd.Close(); err != nil {
			t.Errorf("close data directory %d: %v", keep.id, err)
		}
	}()
	next := coreconsensus.ClusterID{7, 5, 7}
	members, opt := recoverArgs(t, c, nodes, keep, next)
	before := treeOf(t, keep.dir)
	audit := auditOf(t, keep.dir)
	if err := Recover(keep.dd, next, members, opt); !errors.Is(err, coreconsensus.VersionUnsupported) {
		t.Fatalf("Recover over an interrupted install at cluster version %d: %v, want VERSION_UNSUPPORTED", raft.ProtocolMax+1, err)
	}
	after := treeOf(t, keep.dir)
	for p, b := range after {
		if old, ok := before[p]; !ok || !bytes.Equal(old, b) {
			t.Fatalf("a refused Recover wrote %s", p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			t.Fatalf("a refused Recover removed %s", p)
		}
	}
	if got := auditOf(t, keep.dir); !bytes.Equal(got, audit) {
		t.Fatalf("a refused Recover wrote its audit line: %q", got)
	}
}

// auditOf is the audit file of dir, nil when there is none.
func auditOf(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, fileAudit))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return b
}
