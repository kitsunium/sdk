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
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
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
	// Each node's view, so that a run with no leader says why: none of
	// the Windows runs that failed "no leader" did (#587).
	for _, dn := range nodes {
		t.Logf("node %d status %+v %s", dn.id, dn.node.Status(), diskNodeView(dn))
	}
	t.Fatal("no leader")
	return nil
}

// diskNodeView is dn's hard state and raft view, or what did not answer
// within a second: a node with no leader may have its loop or its log
// stuck in a sync, and the report must not wait for it.
func diskNodeView(dn *diskNode) string {
	view := make(chan string, 1)
	go func() {
		dump := "(the loop stopped)"
		dn.node.exec(func() { dump = dn.node.r.Debug() })
		hs, err := dn.dd.Log().HardState()
		view <- fmt.Sprintf("hard state %+v (%v) raft %s", hs, err, dump)
	}()
	select {
	case v := <-view:
		return v
	case <-time.After(time.Second):
		return "(its loop or its log did not answer within a second)"
	}
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
	got, err := ImportObserved(fresh.dd, &ImportParams{Snapshot: bytes.NewReader(snap), Tail: tail, Machine: newKV()}, next, []coreconsensus.MemberValue{self}, opt)
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
		_, err := ImportObserved(fresh.dd, &ImportParams{Snapshot: bytes.NewReader(snap), Tail: mutate(clone()), Machine: newKV()}, next, []coreconsensus.MemberValue{self}, opt)
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

// heldLog is a MemoryLog whose storage actor can be held inside one write:
// once armed, the next TruncateSuffix or Append (as hold says) signals
// entered and waits for release. Readers are never held.
type heldLog struct {
	*MemoryLog
	hold    string
	armed   chan struct{}
	entered chan struct{}
	release chan struct{}
}

func newHeldLog(hold string) *heldLog {
	return &heldLog{MemoryLog: NewMemoryLog(), hold: hold, armed: make(chan struct{}, 1), entered: make(chan struct{}), release: make(chan struct{})}
}

// wait holds the caller when the log is armed for what.
func (l *heldLog) wait(what string) {
	if what != l.hold {
		return
	}
	select {
	case <-l.armed:
		close(l.entered)
		<-l.release
	default:
	}
}

func (l *heldLog) TruncateSuffix(from uint64) error {
	l.wait("truncate")
	return l.MemoryLog.TruncateSuffix(from)
}

func (l *heldLog) Append(entries []coreconsensus.EntryValue, hs *coreconsensus.HardState) error {
	if len(entries) > 0 {
		l.wait("append")
	}
	return l.MemoryLog.Append(entries, hs)
}

// suffixFrom builds the entries [prev+1, prev+n] of term, chained from
// prevChain as a leader of that term would, their commands tagged with tag.
func suffixFrom(prev uint64, prevChain uint32, term uint64, n int, tag string) []coreconsensus.EntryValue {
	es := make([]coreconsensus.EntryValue, n)
	chain := prevChain
	for k := range es {
		e := &es[k]
		e.Position = coreconsensus.PositionValue{Term: term, Index: prev + uint64(k) + 1}
		e.Kind, e.Version = coreconsensus.EntryCommand, 1
		e.Data = fmt.Appendf(nil, "%s%d=x", tag, k)
		e.BodySum = wire.BodySum(e.Data)
		e.ChainSum = wire.ChainSum(chain, e)
		chain = e.ChainSum
	}
	return es
}

// waitFor polls cond until it holds, or fails the test naming what.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// ReadCommitted reads what the STORE holds as committed, never the loop's
// commit alone (#400). A follower holding an uncommitted suffix of term 2
// is sent a conflicting suffix of term 3 with a commit that covers it: the
// loop truncates, appends and commits in memory at once, while the storage
// actor, held, has not rewritten the store. Whether the actor is held
// before the truncation (the store still holds the old suffix) or between
// it and the append (the store holds neither), ReadCommitted returns no
// entry of the old suffix and no error, and once the actor is released it
// returns the new suffix.
func TestReadCommittedNeverReadsASuffixTheStoreHasNotRewritten(t *testing.T) {
	for _, hold := range []string{"truncate", "append"} {
		t.Run(hold, func(t *testing.T) { readCommittedWithTheActorHeld(t, hold) })
	}
}

func readCommittedWithTheActorHeld(t *testing.T, hold string) {
	cid := coreconsensus.ClusterID{4, 0, 0}
	net := NewMemoryNetwork()
	log := newHeldLog(hold)
	var members []coreconsensus.MemberValue
	for i := range 3 {
		var l coreconsensus.LogStore = log
		if i > 0 {
			// The two leaders are never started: only their identities are.
			l = NewMemoryLog()
		}
		id, inc, err := InitLog(l, cid, fmt.Sprintf("slot-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: fmt.Sprintf("slot-%d", i), SPKI: MemorySPKI(id), Voter: true})
	}
	f, a, b := members[0].ID, members[1].ID, members[2].ID
	cfg := Config{
		Cluster: cid, Machine: newKV(), Durability: DurabilityVolatile, Log: log, Snapshots: NewMemorySnapshots(),
		Transport: NewMemoryTransport(net, f), MaxSnapshotBytes: 1 << 30, SnapshotEveryBytes: 1 << 20,
		MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 1, JoinAuthorizer: allowJoin{},
		// The follower never campaigns during the test: it only obeys.
		InitialTiming: coreconsensus.TimingContractSpec{
			Generation: 1, HeartbeatInterval: time.Second,
			ElectionTimeoutMin: time.Hour, ElectionTimeoutMax: 2 * time.Hour, MaxClockDrift: 0.05,
		},
	}
	if err := Bootstrap(context.Background(), new(cfg), members); err != nil {
		t.Fatal(err)
	}
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-log.release:
		default:
			close(log.release)
		}
		if err := n.HardClose(); err != nil {
			t.Errorf("hard close: %v", err)
		}
	})
	_, last := log.Bounds()
	base := must(log.Entries(last, last+1, 1<<20, nil))(t)[0]
	ctx := context.Background()
	send := func(from coreconsensus.NodeID, term, commit uint64, es []coreconsensus.EntryValue) {
		m := &coreconsensus.MessageValue{
			Version: 1, Group: 1, Kind: coreconsensus.MsgApp, From: from, To: f, Term: term,
			Index: base.Position.Index, LogTerm: base.Position.Term, Commit: commit, Entries: es,
		}
		n.deliver(coreconsensus.AuthenticatedPeerValue{ID: from}, appendFrame(m))
	}

	// Term 2: a suffix the store holds, never committed.
	oldTerm := base.Position.Term + 1
	send(a, oldTerm, base.Position.Index, suffixFrom(last, base.ChainSum, oldTerm, 3, "old"))
	waitFor(t, "the old suffix in the store", func() bool {
		_, l := log.Bounds()
		term, err := log.Term(last + 3)
		return l == last+3 && err == nil && term == oldTerm
	})

	// Term 3: a conflicting suffix, committed by the same append.
	log.armed <- struct{}{}
	newTerm := oldTerm + 1
	send(b, newTerm, last+3, suffixFrom(last, base.ChainSum, newTerm, 3, "new"))
	<-log.entered
	waitFor(t, "the loop's commit", func() bool { return n.Status().Commit == last+3 })
	es, err := n.ReadCommitted(ctx, last+1, last+3, 1<<20)
	if err != nil {
		t.Fatalf("held before the %s: an error the store does not justify: %v", hold, err)
	}
	for _, e := range es {
		if e.Position.Term != newTerm {
			t.Fatalf("held before the %s: read %d at term %d, an entry never committed", hold, e.Position.Index, e.Position.Term)
		}
	}

	close(log.release)
	waitFor(t, "the new suffix read as committed", func() bool {
		es, err := n.ReadCommitted(ctx, last+1, last+3, 1<<20)
		if err != nil {
			t.Fatalf("after the release: %v", err)
		}
		return len(es) == 3
	})
	es, _ = n.ReadCommitted(ctx, last+1, last+3, 1<<20)
	for k, e := range es {
		if e.Position.Term != newTerm || string(e.Data) != fmt.Sprintf("new%d=x", k) {
			t.Fatalf("after the release: read %d at term %d: %q", e.Position.Index, e.Position.Term, e.Data)
		}
	}
}

// An entry this node applied is read as committed at once, before the
// storage actor wrote the commit that covers it: Propose answers once its
// entry is applied, and the store's own commit can still be behind it. A
// bound on the store's commit alone would hide the entry a caller just
// waited for (#400).
func TestReadCommittedReturnsAnAppliedEntryAtOnce(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.leader(5 * time.Second)
	for i := range 50 {
		tok := propose(t, l.node, fmt.Sprintf("now%d=1", i))
		es, err := l.node.ReadCommitted(context.Background(), tok.Index, tok.Index, 1<<20)
		if err != nil || len(es) != 1 || es[0].Position != tok.Position() {
			t.Fatalf("proposal %d at %v: read %v, %v", i, tok, es, err)
		}
	}
}
