package consensus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// leaderOf returns the node that leads among nodes.
func leaderOf(t *testing.T, nodes []*diskNode) *diskNode {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, dn := range nodes {
			if dn.node.Status().Role == coreconsensus.RoleLeader {
				return dn
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no leader")
	return nil
}

// ReadCommitted returns the committed log, consecutive from the index asked,
// clamped to the commit, bounded in bytes but never empty when one is due.
func TestReadCommittedReturnsTheCommittedLog(t *testing.T) {
	c, nodes := threeOnDisk(t, 10)
	defer func() {
		for _, dn := range nodes {
			dn.crash(t, c)
		}
	}()
	l := leaderOf(t, nodes)
	commit := l.node.Status().Commit
	ctx := context.Background()
	es, err := l.node.ReadCommitted(ctx, 0, commit+100, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) == 0 || es[0].Position.Index != 1 || es[len(es)-1].Position.Index != commit {
		t.Fatalf("read %d entries, want 1..%d", len(es), commit)
	}
	var cmds []string
	for i, e := range es {
		if e.Position.Index != uint64(i)+1 {
			t.Fatalf("entry %d at position %d", i, e.Position.Index)
		}
		if e.Kind == coreconsensus.EntryCommand {
			cmds = append(cmds, string(e.Data))
		}
	}
	if len(cmds) != 10 || cmds[0] != "k0=v0" || cmds[9] != "k9=v9" {
		t.Fatalf("commands %v", cmds)
	}
	one, err := l.node.ReadCommitted(ctx, 2, commit, 1)
	if err != nil || len(one) != 1 || one[0].Position.Index != 2 {
		t.Fatalf("a byte bound still returns one entry: %v, %v", one, err)
	}
	if none, err := l.node.ReadCommitted(ctx, commit+1, commit+5, 1<<20); err != nil || len(none) != 0 {
		t.Fatalf("nothing past the commit: %v, %v", none, err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := l.node.ReadCommitted(cctx, 1, commit, 1<<20); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled context: %v", err)
	}
}

// OpenSnapshot decodes what SaveSnapshot wrote, without a node, and refuses
// a stream whose bytes or digest differ from its metadata.
func TestOpenSnapshotVerifiesTheStream(t *testing.T) {
	c, nodes := threeOnDisk(t, 5)
	defer func() {
		for _, dn := range nodes {
			dn.crash(t, c)
		}
	}()
	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	meta, err := nodes[0].node.SaveSnapshot(ctx, "op:save", &buf)
	if err != nil {
		t.Fatal(err)
	}
	opt := &RestoreOptions{Expected: meta, MaxDecoded: 1 << 30}
	r, err := OpenSnapshot(bytes.NewReader(buf.Bytes()), opt)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(decoded)) != meta.DecodedSize {
		t.Fatalf("decoded %d bytes, want %d", len(decoded), meta.DecodedSize)
	}
	kv := newKV()
	if err := kv.Restore(bytes.NewReader(decoded), meta); err != nil {
		t.Fatal(err)
	}
	if v, _ := kv.get("k4"); v != "v4" {
		t.Fatalf("k4=%q", v)
	}

	bad := opt.Expected
	bad.SHA256[0] ^= 1
	r, _ = OpenSnapshot(bytes.NewReader(buf.Bytes()), &RestoreOptions{Expected: bad, MaxDecoded: 1 << 30})
	if _, err := io.ReadAll(r); !errors.Is(err, coreconsensus.SnapshotCorrupt) {
		t.Fatalf("a digest that differs: %v", err)
	}
	cut := buf.Bytes()[:buf.Len()-3]
	r, _ = OpenSnapshot(bytes.NewReader(cut), opt)
	if _, err := io.ReadAll(r); !errors.Is(err, coreconsensus.SnapshotCorrupt) {
		t.Fatalf("a stream cut short: %v", err)
	}
	if _, err := OpenSnapshot(bytes.NewReader(buf.Bytes()), &RestoreOptions{Expected: meta}); !errors.Is(err, coreconsensus.RestoreRefused) {
		t.Fatalf("no bound: %v", err)
	}
}

// importFixture saves a snapshot, writes more after it, reads the tail the
// leader committed after the snapshot, and stops the cluster.
func importFixture(t *testing.T) (coreconsensus.SnapshotMetaValue, []byte, []coreconsensus.EntryValue) {
	c, nodes := threeOnDisk(t, 10)
	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	l := leaderOf(t, nodes)
	meta, err := l.node.SaveSnapshot(ctx, "op:save", &buf)
	if err != nil {
		t.Fatal(err)
	}
	var tok coreconsensus.TokenValue
	for i := range 5 {
		tok = propose(t, l.node, fmt.Sprintf("t%d=after", i))
	}
	c.waitAppliedDisk(nodes, tok.Index)
	tail, err := l.node.ReadCommitted(ctx, meta.Position.Index+1, tok.Index, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	return meta, buf.Bytes(), tail
}

func freshJoining(t *testing.T, next coreconsensus.ClusterID) (*diskNode, coreconsensus.MemberValue) {
	fresh := &diskNode{dir: t.TempDir()}
	fresh.dd = openDir(t, fresh.dir, 16<<10)
	id, inc, err := Init(fresh.dd, next, "observed")
	if err != nil {
		t.Fatal(err)
	}
	fresh.id = id
	return fresh, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: "observed", SPKI: MemorySPKI(id), Voter: true}
}

// ImportObserved starts a new history from a snapshot and the verified tail
// after it: the state holds the tail's writes, and the new history keeps the
// tail's last index.
func TestImportObservedReplaysAVerifiedTail(t *testing.T) {
	meta, snap, tail := importFixture(t)
	next := coreconsensus.ClusterID{9}
	fresh, self := freshJoining(t, next)
	opt := &RestoreOptions{Expected: meta, Confirm: next.String(), Operator: "op:import", MaxEncoded: 1 << 30, MaxDecoded: 1 << 30}
	got, err := ImportObserved(fresh.dd, bytes.NewReader(snap), tail, newKV(), next, []coreconsensus.MemberValue{self}, opt)
	if err != nil {
		t.Fatal(err)
	}
	last := tail[len(tail)-1].Position
	if got.Position != last || got.Log.KeptIndex != last.Index || got.Log.Predecessor != meta.Log.Cluster || got.Log.Cluster != next {
		t.Fatalf("imported at %+v, log %+v; want %+v kept, predecessor %s", got.Position, got.Log, last, meta.Log.Cluster)
	}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: next}
	fresh.sm = newKV()
	cfg := diskConfig(c, fresh)
	cfg.MinVoters = 1
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fresh.node = n
	defer fresh.crash(t, c)
	for k, want := range map[string]string{"k9": "v9", "t4": "after"} {
		if v, _ := fresh.sm.get(k); v != want {
			t.Fatalf("imported state: %s=%q, want %q", k, v, want)
		}
	}
	propose(t, n, "after=import")
}

// A tail that does not continue the snapshot is refused before anything is
// written: a gap, a payload that differs from its sum, a broken chain, a
// term that goes back, an entry that changes the engine's own state.
func TestImportObservedRefusesATamperedTail(t *testing.T) {
	meta, snap, tail := importFixture(t)
	next := coreconsensus.ClusterID{10}
	clone := func() []coreconsensus.EntryValue {
		out := make([]coreconsensus.EntryValue, len(tail))
		copy(out, tail)
		return out
	}
	cases := map[string]func([]coreconsensus.EntryValue) []coreconsensus.EntryValue{
		"gap": func(es []coreconsensus.EntryValue) []coreconsensus.EntryValue { return append(es[:1], es[2:]...) },
		"payload": func(es []coreconsensus.EntryValue) []coreconsensus.EntryValue {
			es[1].Data = []byte("t1=evil")
			return es
		},
		"chain": func(es []coreconsensus.EntryValue) []coreconsensus.EntryValue {
			es[2].ChainSum++
			return es
		},
		"term": func(es []coreconsensus.EntryValue) []coreconsensus.EntryValue {
			es[0].Position.Term = meta.Position.Term - 1
			return es
		},
		"engine state": func(es []coreconsensus.EntryValue) []coreconsensus.EntryValue {
			es[0].Kind = coreconsensus.EntryClusterVersion
			return es
		},
	}
	for name, mutate := range cases {
		fresh, self := freshJoining(t, next)
		opt := &RestoreOptions{Expected: meta, Confirm: next.String(), Operator: "op:import", MaxEncoded: 1 << 30, MaxDecoded: 1 << 30}
		_, err := ImportObserved(fresh.dd, bytes.NewReader(snap), mutate(clone()), newKV(), next, []coreconsensus.MemberValue{self}, opt)
		if !errors.Is(err, coreconsensus.RestoreRefused) {
			t.Fatalf("%s: %v", name, err)
		}
		rep, err := Inspect(fresh.dd)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Snapshots) != 0 || rep.Stamp.State != coreconsensus.StampJoining {
			t.Fatalf("%s: the directory was written: %+v", name, rep)
		}
		if err := fresh.dd.Close(); err != nil && !strings.Contains(err.Error(), "closed") {
			t.Fatal(err)
		}
	}
}
