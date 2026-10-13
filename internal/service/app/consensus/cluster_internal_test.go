package consensus

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// kv is a key-value state machine held in memory: Apply sets "k=v" and
// returns the previous value.
type kv struct {
	mu   sync.Mutex
	m    map[string]string
	last uint64
}

func newKV() *kv { return &kv{m: map[string]string{}} }

func (s *kv) Apply(e coreconsensus.Entry) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, v, _ := bytes.Cut(e.Data, []byte("="))
	prev := s.m[string(k)]
	s.m[string(k)] = string(v)
	s.last = e.Position.Index
	return prev
}

func (s *kv) get(k string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k]
	return v, ok
}

type kvSnap struct{ data []byte }

func (s kvSnap) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(s.data)
	return int64(n), err
}
func (kvSnap) Release() {}

func (s *kv) Snapshot() (coreconsensus.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b []byte
	keys := make([]string, 0, len(s.m))
	for k := range s.m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		b = binary.AppendUvarint(b, uint64(len(k)))
		b = append(b, k...)
		b = binary.AppendUvarint(b, uint64(len(s.m[k])))
		b = append(b, s.m[k]...)
	}
	return kvSnap{data: b}, nil
}

func (s *kv) Restore(r io.Reader, _ coreconsensus.SnapshotMeta) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m := map[string]string{}
	for len(b) > 0 {
		n, k := binary.Uvarint(b)
		b = b[k:]
		key := string(b[:n])
		b = b[n:]
		n, k = binary.Uvarint(b)
		b = b[k:]
		m[key] = string(b[:n])
		b = b[n:]
	}
	s.mu.Lock()
	s.m = m
	s.mu.Unlock()
	return nil
}

type testNode struct {
	id    coreconsensus.NodeID
	log   *MemoryLog
	snaps *MemorySnapshots
	sm    *kv
	node  *Node
}

type cluster struct {
	t       *testing.T
	net     *MemoryNetwork
	cid     coreconsensus.ClusterID
	nodes   []*testNode
	members []coreconsensus.Member
	every   uint64
	recent  int64
	size    int
}

func testTimingFast() coreconsensus.TimingContract {
	return coreconsensus.TimingContract{
		Generation: 1, HeartbeatInterval: 10 * time.Millisecond,
		ElectionTimeoutMin: 100 * time.Millisecond, ElectionTimeoutMax: 200 * time.Millisecond, MaxClockDrift: 0.05,
	}
}

func (c *cluster) config(tn *testNode) Config {
	return Config{
		Cluster: c.cid, Machine: tn.sm, Durability: DurabilityVolatile, Log: tn.log, Snapshots: tn.snaps,
		Transport: c.net.Transport(tn.id), InitialTiming: testTimingFast(), MaxSnapshotBytes: 1 << 30,
		SnapshotEveryBytes: 1 << 20, MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 1,
		SnapshotEvery: c.every, SnapshotTrailing: 4, JoinAuthorizer: allowJoin{}, RecentEntriesBytes: c.recent,
	}
}

type allowJoin struct{}

func (allowJoin) Authorize(_ context.Context, r coreconsensus.JoinRequest) (coreconsensus.Operator, error) {
	return coreconsensus.Operator("automation:" + r.Slot), nil
}

func newCluster(t *testing.T, size int, every uint64) *cluster {
	return newClusterWith(t, &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 3}, every: every, size: size})
}

func newClusterWith(t *testing.T, c *cluster) *cluster {
	size := c.size
	if size == 0 {
		size = 3
	}
	for i := 0; i < size; i++ {
		tn := &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
		id, inc, err := InitLog(tn.log, c.cid, fmt.Sprintf("slot-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		tn.id = id
		c.nodes = append(c.nodes, tn)
		c.members = append(c.members, coreconsensus.Member{
			ID: id, Incarnation: inc, Slot: fmt.Sprintf("slot-%d", i),
			SPKI: MemorySPKI(id), Voter: true,
		})
	}
	for _, tn := range c.nodes {
		if err := Bootstrap(context.Background(), c.config(tn), c.members); err != nil {
			t.Fatal(err)
		}
		// Rerunning with the identical list is an idempotent success.
		if err := Bootstrap(context.Background(), c.config(tn), c.members); err != nil {
			t.Fatal(err)
		}
	}
	for _, tn := range c.nodes {
		c.start(tn)
	}
	t.Cleanup(func() {
		for _, tn := range c.nodes {
			if tn.node != nil {
				_ = tn.node.HardClose()
			}
		}
	})
	return c
}

func (c *cluster) start(tn *testNode) {
	tn.sm = newKV()
	n, err := NewRaft(c.config(tn))
	if err != nil {
		c.t.Fatalf("NewRaft %d: %v", tn.id, err)
	}
	tn.node = n
}

func (c *cluster) kill(tn *testNode) {
	_ = tn.node.HardClose()
	_ = c.net.Transport(tn.id).Close()
	tn.node = nil
}

func (c *cluster) leader(timeout time.Duration) *testNode {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var lead *testNode
		var term uint64
		for _, tn := range c.nodes {
			if tn.node == nil {
				continue
			}
			st := tn.node.Status()
			if st.Role == coreconsensus.RoleLeader && st.Term >= term {
				lead, term = tn, st.Term
			}
		}
		if lead != nil {
			return lead
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.t.Fatal("no leader")
	return nil
}

func (c *cluster) waitApplied(index uint64, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for _, tn := range c.nodes {
		if tn.node == nil {
			continue
		}
		for tn.node.Status().Applied < index {
			if time.Now().After(deadline) {
				for _, x := range c.nodes {
					if x.node != nil {
						var dump string
						x.node.exec(func() { dump = x.node.r.Debug() })
						f, l := x.log.Bounds()
						c.t.Logf("node %d status %+v log [%d,%d] raft %s", x.id, x.node.Status(), f, l, dump)
					}
				}
				c.t.Fatalf("node %d applied %d < %d", tn.id, tn.node.Status().Applied, index)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func propose(t *testing.T, n *Node, kv string) coreconsensus.Token {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		tok, _, err := n.Propose(ctx, []byte(kv))
		if err == nil {
			return tok
		}
		if errors.Is(err, coreconsensus.NotLeader) && ctx.Err() == nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		t.Fatalf("propose %q: %v", kv, err)
	}
}

func TestClusterElectsReplicatesAndSurvivesLeaderLoss(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.leader(5 * time.Second)

	// On the leader, and forwarded from a follower.
	tok := propose(t, l.node, "a=1")
	var follower *testNode
	for _, tn := range c.nodes {
		if tn != l {
			follower = tn
			break
		}
	}
	tok2 := propose(t, follower.node, "b=2")
	if tok2.Index <= tok.Index {
		t.Fatalf("tokens out of order: %v then %v", tok, tok2)
	}
	if tok.Cluster != c.cid || tok.Epoch != 1 {
		t.Fatalf("token lineage %v", tok)
	}
	c.waitApplied(tok2.Index, 5*time.Second)
	for _, tn := range c.nodes {
		if v, _ := tn.sm.get("b"); v != "2" {
			t.Fatalf("node %d: b=%q", tn.id, v)
		}
	}

	// A barrier on a follower reflects every write committed before it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := follower.node.Barrier(ctx); err != nil {
		t.Fatal(err)
	}

	// The result of Apply comes back to the proposer.
	_, prev, err := ProposeAs[string](ctx, l.node, []byte("a=3"))
	if err != nil || prev != "1" {
		t.Fatalf("ProposeAs: %q %v", prev, err)
	}

	// Kill the leader: the other two elect and keep committing.
	old := l
	c.kill(old)
	l2 := c.leader(5 * time.Second)
	if l2 == old {
		t.Fatal("dead leader still leads")
	}
	tok3 := propose(t, l2.node, "c=4")

	// Restart the old leader over its log: it replays and catches up.
	c.start(old)
	c.waitApplied(tok3.Index, 5*time.Second)
	if v, _ := old.sm.get("c"); v != "4" {
		t.Fatalf("restarted node did not catch up: c=%q", v)
	}
	if v, _ := old.sm.get("a"); v != "3" {
		t.Fatalf("restarted node replay: a=%q", v)
	}
	if err := old.node.WaitApplied(ctx, tok3); err != nil {
		t.Fatal(err)
	}
	other := tok3
	other.Epoch = 9
	if err := old.node.WaitApplied(ctx, other); !errors.Is(err, coreconsensus.LineageMismatch) {
		t.Fatalf("another lineage: %v", err)
	}
}

func TestClusterSnapshotInstallsOnALaggingNode(t *testing.T) {
	c := newCluster(t, 3, 20)
	l := c.leader(5 * time.Second)
	var lag *testNode
	for _, tn := range c.nodes {
		if tn != l {
			lag = tn
			break
		}
	}
	c.kill(lag)
	var tok coreconsensus.Token
	for i := 0; i < 200; i++ {
		tok = propose(t, l.node, fmt.Sprintf("k%d=%d", i, i))
	}
	// Wait for compaction past what the lagging node holds.
	deadline := time.Now().Add(5 * time.Second)
	for {
		first, _ := l.log.Bounds()
		if first > 10 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("leader log never compacted (first %d)", first)
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.start(lag)
	c.waitApplied(tok.Index, 10*time.Second)
	if v, _ := lag.sm.get("k199"); v != "199" {
		t.Fatalf("lagging node: k199=%q", v)
	}
	if metas, _ := lag.snaps.List(); len(metas) == 0 {
		t.Fatal("the lagging node caught up without a snapshot")
	}
}

func TestClusterMembershipThroughLearners(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.leader(5 * time.Second)
	propose(t, l.node, "x=1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// A fourth node joins through JOIN, is promoted, and an old one removed.
	tn := &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
	id, _, err := InitLog(tn.log, c.cid, "slot-new")
	if err != nil {
		t.Fatal(err)
	}
	tn.id = id
	c.nodes = append(c.nodes, tn)
	c.start(tn)
	st, _ := tn.log.Stamp()
	if _, err := c.net.Transport(id).Join(ctx, coreconsensus.JoinConfig{Stamp: st, Address: "mem"}); err != nil {
		t.Fatalf("join: %v", err)
	}
	tok := propose(t, l.node, "y=2")
	c.waitApplied(tok.Index, 5*time.Second)
	if v, _ := tn.sm.get("y"); v != "2" {
		t.Fatal("learner did not receive the log")
	}
	if err := l.node.Promote(ctx, "op:test", id); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := l.node.Promote(ctx, "op:test", id); !errors.Is(err, coreconsensus.MembershipInvalid) {
		t.Fatalf("second promote: %v", err)
	}
	var victim coreconsensus.NodeID
	for _, x := range c.nodes {
		if x != l && x != tn {
			victim = x.id
			break
		}
	}
	// Removal submitted on a follower is forwarded to the leader (Q8).
	if err := tn.node.Remove(ctx, "op:test", victim); err != nil {
		t.Fatalf("remove: %v", err)
	}
	m := l.node.Membership()
	if _, ok := m.Member(victim); ok || !m.IsRetired(victim) {
		t.Fatalf("victim still a member: %+v", m)
	}
	vm := coreconsensus.Member{ID: victim, SPKI: [32]byte{1}}
	if err := l.node.AddLearner(ctx, "op:test", vm); !errors.Is(err, coreconsensus.NodeIDRetired) {
		t.Fatalf("retired id re-added: %v", err)
	}
	tok = propose(t, l.node, "z=3")
	if tok.Index == 0 {
		t.Fatal("no commit after the replacement")
	}
}

func TestClusterTransferLeadership(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.leader(5 * time.Second)
	propose(t, l.node, "a=1")
	var to *testNode
	for _, tn := range c.nodes {
		if tn != l {
			to = tn
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.node.TransferLeadership(ctx, "op:test", to.id); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if st := to.node.Status(); st.Role != coreconsensus.RoleLeader {
		// The watch saw the leader; give the target's own status a turn.
		time.Sleep(50 * time.Millisecond)
		if st = to.node.Status(); st.Role != coreconsensus.RoleLeader {
			t.Fatalf("target role %v", st.Role)
		}
	}
	propose(t, to.node, "b=2")
}

func TestStorageFailureStopsTheNode(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.leader(5 * time.Second)
	l.log.FailSync(errors.New("injected fdatasync failure"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, _ = l.node.Propose(ctx, []byte("a=1"))
	deadline := time.Now().Add(2 * time.Second)
	for {
		if r, ok := l.node.StopReason(); ok {
			if c, _ := errs.CodeOf(r.Err); c != coreconsensus.CodeStorageFailed {
				t.Fatalf("stop reason %v", r.Err)
			}
			if r.ExitCode() != 74 {
				t.Fatalf("exit code %d", r.ExitCode())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("node did not stop on a failed sync")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, _, err := l.node.Propose(ctx, []byte("a=2")); !errors.Is(err, coreconsensus.NodeStopped) {
		t.Fatalf("propose after stop: %v", err)
	}
}

// The engine trusts the transport's authenticated peer, never the frame's
// From (R7-05): a frame naming another sender is dropped and counted.
func TestEngineDropsAFrameWhoseFromIsNotThePeer(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.leader(5 * time.Second)
	var f *testNode
	for _, tn := range c.nodes {
		if tn != l {
			f = tn
			break
		}
	}
	before := f.node.Health().DroppedFrames
	m := coreconsensus.Message{Version: 1, Group: 1, Kind: coreconsensus.MsgTimeoutNow, From: l.id, To: f.id, Term: l.node.Status().Term}
	frame := appendFrame(&m)
	var liar coreconsensus.NodeID
	for _, tn := range c.nodes {
		if tn != l && tn != f {
			liar = tn.id
		}
	}
	f.node.deliver(coreconsensus.AuthenticatedPeer{ID: liar}, frame)
	if got := f.node.Health().DroppedFrames; got != before+1 {
		t.Fatalf("dropped %d, want %d", got, before+1)
	}
	time.Sleep(100 * time.Millisecond)
	if f.node.Status().Role == coreconsensus.RoleLeader {
		t.Fatal("a forged TimeoutNow was obeyed")
	}
}

func appendFrame(m *coreconsensus.Message) []byte { return wire.AppendMessage(nil, m) }

// A transfer submitted on a follower succeeds, or fails with TRANSFER_FAILED
// and the old leader resumes; never OUTCOME_UNKNOWN (D8). NOT_LEADER is the
// other answer the design allows: a transfer refuses new proposals from the
// moment it starts, and nothing was appended, so it is retried (ADR 0152,
// "A transfer loses nothing in flight"). Every round must still end with the
// leadership on its target, within a bounded time.
func TestClusterTransferSubmittedOnAFollower(t *testing.T) {
	c := newCluster(t, 3, 0)
	for round := 0; round < 10; round++ {
		// A follower that has not yet heard from a just-elected leader
		// answers NOT_LEADER at once, so the bound is a deadline with a
		// pause between attempts, not a count.
		deadline := time.Now().Add(10 * time.Second)
		var last error
		for attempt := 0; ; attempt++ {
			if attempt > 0 {
				if time.Now().After(deadline) {
					t.Fatalf("round %d: no transfer from a follower completed in %d attempts, last: %v", round, attempt, last)
				}
				time.Sleep(20 * time.Millisecond)
			}
			l := c.leader(5 * time.Second)
			propose(t, l.node, fmt.Sprintf("r=%d", round))
			var f, to *testNode
			for _, tn := range c.nodes {
				if tn == l {
					continue
				}
				if f == nil {
					f = tn
				} else {
					to = tn
				}
			}
			if round%2 == 1 {
				to = f // the follower moves leadership to itself
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := f.node.TransferLeadership(ctx, "op:test", to.id)
			cancel()
			if errors.Is(err, coreconsensus.TransferFailed) || errors.Is(err, coreconsensus.NotLeader) {
				last = err
				// Allowed and retryable; the cluster must still have a
				// leader, which c.leader fails the test without.
				c.leader(5 * time.Second)
				continue
			}
			if err != nil {
				t.Fatalf("round %d: transfer from a follower: %v", round, err)
			}
			// The transfer returned once f SAW `to` lead; each node's Status
			// is its view after its own last turn, so the old leader may
			// still publish its old term's leadership for a turn after `to`
			// won. The leader must be `to` once the views settle.
			settle := time.Now().Add(5 * time.Second)
			for {
				nl := c.leader(5 * time.Second)
				if nl == to {
					break
				}
				if time.Now().After(settle) {
					t.Fatalf("round %d: leader is %d, want %d", round, nl.id, to.id)
				}
				time.Sleep(5 * time.Millisecond)
			}
			break
		}
	}
}

func TestHealthReportsFailureToleranceAndPeers(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.leader(5 * time.Second)
	tok := propose(t, l.node, "a=1")
	c.waitApplied(tok.Index, 5*time.Second)
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("%s: %+v", what, l.node.Health())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("tolerance 1 with three voters", func() bool {
		h := l.node.Health()
		return h.FailureTolerance == 1 && h.QuorumReachable && len(h.Peers) == 2 &&
			!h.Peers[0].Unreachable && !h.Peers[1].Unreachable && h.Peers[0].Lag == 0
	})
	var f *testNode
	for _, tn := range c.nodes {
		if tn != l {
			f = tn
			break
		}
	}
	if h := f.node.Health(); h.FailureTolerance != -1 || h.Peers != nil || !h.HasLeader || !h.Ready {
		t.Fatalf("follower health %+v", h)
	}
	c.net.Isolate(f.id, true)
	propose(t, l.node, "b=2")
	waitFor("the isolated peer is unreachable and lags", func() bool {
		h := l.node.Health()
		for _, p := range h.Peers {
			if p.ID == f.id {
				return h.FailureTolerance == 0 && p.Unreachable && p.Lag > 0
			}
		}
		return false
	})
	c.net.Isolate(f.id, false)
}

// R7-18: every snapshot of a node fails its checksum. It starts with no
// table, never campaigns, asks for an install and catches up through it.
func TestClusterNodeWithoutAValidSnapshotAsksForAnInstall(t *testing.T) {
	c := newCluster(t, 3, 20)
	l := c.leader(5 * time.Second)
	var tok coreconsensus.Token
	for i := 0; i < 120; i++ {
		tok = propose(t, l.node, fmt.Sprintf("k%d=%d", i, i))
	}
	c.waitApplied(tok.Index, 5*time.Second)
	var f *testNode
	for _, tn := range c.nodes {
		if tn != l {
			f = tn
			break
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		first, _ := f.log.Bounds()
		metas, _ := f.snaps.List()
		if first > 1 && len(metas) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the follower never compacted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.kill(f)
	f.snaps.mu.Lock()
	for _, s := range f.snaps.done {
		s.data[len(s.data)/2] ^= 0xff
	}
	f.snaps.mu.Unlock()
	c.start(f)
	if !f.node.r.NoTable() && f.node.Status().Applied == 0 {
		t.Fatal("setup: the node did not start without a table")
	}
	for i := 120; i < 130; i++ {
		tok = propose(t, l.node, fmt.Sprintf("k%d=%d", i, i))
	}
	c.waitApplied(tok.Index, 10*time.Second)
	if v, _ := f.sm.get("k5"); v != "5" {
		t.Fatalf("the install did not restore the table: k5=%q", v)
	}
	if v, _ := f.sm.get("k129"); v != "129" {
		t.Fatalf("no catch-up after the install: k129=%q", v)
	}
}

// With a small RecentEntriesBytes, a follower that comes back far behind is
// fed from the leader's store, not from its memory.
func TestClusterLaggingFollowerFedFromTheStore(t *testing.T) {
	c := newClusterWith(t, &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{4}, recent: 1024})
	l := c.leader(5 * time.Second)
	var f *testNode
	for _, tn := range c.nodes {
		if tn != l {
			f = tn
			break
		}
	}
	c.kill(f)
	var tok coreconsensus.Token
	for i := 0; i < 300; i++ {
		tok = propose(t, l.node, fmt.Sprintf("key-%d=%0100d", i, i))
	}
	c.start(f)
	c.waitApplied(tok.Index, 10*time.Second)
	if v, _ := f.sm.get("key-7"); v != fmt.Sprintf("%0100d", 7) {
		t.Fatalf("lagging follower: key-7=%q", v)
	}
}

// A loop turn longer than the drift budget drops the lease (D9, R7-20).
func TestLongLoopTurnDropsTheLease(t *testing.T) {
	c := newCluster(t, 3, 0)
	for _, tn := range c.nodes {
		_ = tn.node.HardClose()
	}
	for _, tn := range c.nodes {
		_ = c.net.Transport(tn.id).Close()
		tn.sm = newKV()
		cfg := c.config(tn)
		cfg.Reads = ReadLease
		// A 50 ms budget (1 s × 0.05): ordinary turns under -race stay
		// below it, the 200 ms pause below does not.
		cfg.InitialTiming = coreconsensus.TimingContract{
			Generation: 1, HeartbeatInterval: 50 * time.Millisecond,
			ElectionTimeoutMin: time.Second, ElectionTimeoutMax: 2 * time.Second, MaxClockDrift: 0.05,
		}
		n, err := NewRaft(cfg)
		if err != nil {
			t.Fatal(err)
		}
		tn.node = n
	}
	l := c.leader(5 * time.Second)
	propose(t, l.node, "a=1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := l.node.Barrier(ctx); err != nil {
		t.Fatal(err)
	}
	valid := false
	deadline := time.Now().Add(5 * time.Second)
	for !valid && time.Now().Before(deadline) {
		l.node.exec(func() { valid = l.node.r.LeaseValid(l.node.clk.Now()) })
		time.Sleep(5 * time.Millisecond)
	}
	if !valid {
		t.Fatal("setup: no lease")
	}
	l.node.exec(func() { time.Sleep(200 * time.Millisecond) })
	l.node.exec(func() { valid = l.node.r.LeaseValid(l.node.clk.Now()) })
	if valid || l.node.Health().LongTurns == 0 {
		t.Fatalf("the lease survived a long turn (long turns %d)", l.node.Health().LongTurns)
	}
}

// One JOIN per slot per election timeout (R7-42).
func TestJoinIsRateLimitedPerSlot(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.leader(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peer := coreconsensus.AuthenticatedPeer{Slot: "burst", SPKI: [32]byte{7}}
	req := coreconsensus.JoinRequest{Cluster: c.cid, ID: 991, Slot: "burst"}
	if _, code := l.node.join(ctx, peer, req); code != 0 {
		t.Fatalf("first join: %v", code)
	}
	req.ID = 992
	if _, code := l.node.join(ctx, peer, req); code != coreconsensus.CodeJoinRefused {
		t.Fatalf("second join within the window: %v", code)
	}
	if l.node.Health().JoinsRateLimited != 1 {
		t.Fatal("rate-limited join not counted")
	}
}

// gatedKV blocks Apply until released, so the apply lag grows.
type gatedKV struct {
	*kv
	gate chan struct{}
}

func (g gatedKV) Apply(e coreconsensus.Entry) any {
	<-g.gate
	return g.kv.Apply(e)
}

// D6, R1-14: once the gap between commit and applied reaches
// MaxApplyLagBytes, the leader refuses new proposals with OVERLOADED, and
// accepts them again once apply catches up.
func TestApplyLagBackpressure(t *testing.T) {
	c := newCluster(t, 3, 0)
	gate := make(chan struct{})
	for _, tn := range c.nodes {
		_ = tn.node.HardClose()
		_ = c.net.Transport(tn.id).Close()
	}
	for _, tn := range c.nodes {
		tn.sm = newKV()
		cfg := c.config(tn)
		cfg.Machine = gatedKV{kv: tn.sm, gate: gate}
		cfg.MaxApplyLagBytes = 2048
		n, err := NewRaft(cfg)
		if err != nil {
			t.Fatal(err)
		}
		tn.node = n
	}
	l := c.leader(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	payload := strings.Repeat("x", 300)
	overloaded := false
	for i := 0; i < 100 && !overloaded; i++ {
		pctx, pcancel := context.WithTimeout(ctx, 30*time.Millisecond)
		_, _, err := l.node.Propose(pctx, []byte(fmt.Sprintf("k%d=%s", i, payload)))
		pcancel()
		overloaded = errors.Is(err, coreconsensus.Overloaded)
	}
	if !overloaded {
		t.Fatal("no OVERLOADED while apply was blocked")
	}
	// A command forwarded by a follower meets the same backpressure
	// (review M2).
	for _, tn := range c.nodes {
		if tn == l {
			continue
		}
		fctx, fcancel := context.WithTimeout(ctx, 2*time.Second)
		_, _, err := tn.node.Propose(fctx, []byte("forwarded=1"))
		fcancel()
		if !errors.Is(err, coreconsensus.Overloaded) {
			t.Fatalf("forwarded under backpressure: %v", err)
		}
		break
	}
	close(gate)
	for {
		_, _, err := l.node.Propose(ctx, []byte("after=1"))
		if err == nil {
			break
		}
		if !errors.Is(err, coreconsensus.Overloaded) {
			t.Fatalf("after apply caught up: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A crash right after an installed snapshot is published, before the
// state machine is restored and the log moved onto it: the restart finishes
// the install from the published snapshot (D7).
func TestInstallResumesAfterACrashAtPublication(t *testing.T) {
	c := newCluster(t, 3, 20)
	l := c.leader(5 * time.Second)
	var lag *testNode
	for _, tn := range c.nodes {
		if tn != l {
			lag = tn
			break
		}
	}
	c.kill(lag)
	var tok coreconsensus.Token
	for i := 0; i < 200; i++ {
		tok = propose(t, l.node, fmt.Sprintf("k%d=%d", i, i))
	}
	deadline := time.Now().Add(5 * time.Second)
	for first, _ := l.log.Bounds(); first <= 10; first, _ = l.log.Bounds() {
		if time.Now().After(deadline) {
			t.Fatal("leader never compacted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.start(lag)
	lag.node.crashAfterPublish.Store(true)
	deadline = time.Now().Add(10 * time.Second)
	for !lag.node.stopped.Load() {
		if time.Now().After(deadline) {
			t.Fatal("no install reached publication")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if metas, _ := lag.snaps.List(); len(metas) == 0 {
		t.Fatal("nothing was published before the crash")
	}
	c.kill(lag)
	c.start(lag)
	c.waitApplied(tok.Index, 10*time.Second)
	if v, _ := lag.sm.get("k199"); v != "199" {
		t.Fatalf("after resuming the install: k199=%q", v)
	}
}

// staleSnapshot runs a cluster until a follower applied past the leader's
// newest snapshot, and returns that snapshot's meta and encoded body.
func staleSnapshot(t *testing.T) (*cluster, *testNode, *testNode, coreconsensus.SnapshotMeta, []byte, coreconsensus.Token) {
	c := newCluster(t, 3, 20)
	l := c.leader(5 * time.Second)
	var f *testNode
	for _, tn := range c.nodes {
		if tn != l {
			f = tn
			break
		}
	}
	for i := 0; i < 60; i++ {
		propose(t, l.node, fmt.Sprintf("k%d=%d", i, i))
	}
	c.waitApplied(l.node.Status().Commit, 5*time.Second)
	metas, err := l.snaps.List()
	if err != nil || len(metas) == 0 {
		t.Fatalf("no leader snapshot: %v", err)
	}
	meta := metas[0]
	h, err := l.snaps.Open(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, meta.EncodedSize)
	if _, err := h.ReadAt(body, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	h.Close()
	// Entries past the snapshot that the receiver applies before the body
	// arrives.
	var tok coreconsensus.Token
	for i := 60; i < 80; i++ {
		tok = propose(t, l.node, fmt.Sprintf("k%d=%d", i, i))
	}
	c.waitApplied(tok.Index, 5*time.Second)
	if meta.Position.Index >= tok.Index {
		t.Fatalf("setup: snapshot %d not behind the applied index %d", meta.Position.Index, tok.Index)
	}
	meta.ID = "h3-" + meta.ID
	return c, l, f, meta, body, tok
}

// checkNothingLost requires every key the cluster committed on f.
func checkNothingLost(t *testing.T, c *cluster, l, f *testNode) {
	t.Helper()
	tok := propose(t, l.node, "last=1")
	c.waitApplied(tok.Index, 5*time.Second)
	for i := 0; i < 80; i++ {
		if v, _ := f.sm.get(fmt.Sprintf("k%d", i)); v != fmt.Sprint(i) {
			t.Fatalf("receiver lost the committed k%d (%q) to a stale install", i, v)
		}
	}
}

// An offer accepted while the receiver was behind, whose body arrives after
// the receiver committed and applied past the snapshot through a new
// leader's appends: the install is refused before it is published (review
// H3).
func TestInstallAfterTheReceiverMovedPastTheSnapshot(t *testing.T) {
	c, l, f, meta, body, tok := staleSnapshot(t)
	// The spool an earlier offer opened, when the receiver was behind.
	sink, _, err := f.snaps.Create(meta.ID, meta)
	if err != nil {
		t.Fatal(err)
	}
	f.node.snapState.mu.Lock()
	if f.node.snapState.spools == nil {
		f.node.snapState.spools = map[string]coreconsensus.SnapshotSink{}
	}
	f.node.snapState.spools[meta.ID] = sink
	f.node.snapState.mu.Unlock()
	err = f.node.install(coreconsensus.InstallRequest{
		Ctx:   context.Background(),
		From:  coreconsensus.AuthenticatedPeer{ID: l.id},
		Offer: coreconsensus.InstallOffer{Term: l.node.Status().Term, SnapshotID: meta.ID, Meta: meta},
		Body:  bytes.NewReader(body),
	})
	if !errors.Is(err, coreconsensus.AdminPreconditionFailed) {
		t.Fatalf("stale install: %v", err)
	}
	if got := f.node.Status().Applied; got < tok.Index {
		t.Fatalf("the install moved applied back to %d from %d", got, tok.Index)
	}
	checkNothingLost(t, c, l, f)
}

// The last guard, on the apply goroutine: a restore queued for a snapshot
// the machine already passed leaves the machine as it is (review H3).
func TestRestoreNeverRollsTheMachineBack(t *testing.T) {
	c, l, f, meta, body, tok := staleSnapshot(t)
	sink, _, err := f.snaps.Create(meta.ID, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := sink.Commit(meta); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	f.node.aq.push(applyItem{restore: &restoreReq{meta: meta, done: done}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := f.node.Status().Applied; got < tok.Index {
		t.Fatalf("the restore moved applied back to %d from %d", got, tok.Index)
	}
	checkNothingLost(t, c, l, f)
}

// smallFrames is a transport whose frames are smaller than a batch.
type smallFrames struct{ coreconsensus.Transport }

func (smallFrames) maxFrameBytes() int { return 64 << 10 }

// A batch the transport cannot carry would stall every follower it is sent
// to: the engine refuses that configuration at construction (review M2).
func TestBatchLargerThanAFrameIsRefused(t *testing.T) {
	c := newCluster(t, 1, 0)
	tn := c.nodes[0]
	c.kill(tn)
	cfg := c.config(tn)
	cfg.Transport = smallFrames{cfg.Transport}
	if _, err := NewRaft(cfg); !errors.Is(err, coreconsensus.ConsensusMisconfigured) {
		t.Fatalf("a batch above the frame limit: %v", err)
	}
}

// An append that carries an entry kind this version does not know stops
// the follower with VERSION_UNSUPPORTED (review M3).
func TestUnknownEntryKindInAnAppendStopsTheNode(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.leader(5 * time.Second)
	tok := propose(t, l.node, "a=1")
	c.waitApplied(tok.Index, 5*time.Second)
	var f *testNode
	for _, tn := range c.nodes {
		if tn != l {
			f = tn
			break
		}
	}
	prev, err := f.log.Entries(tok.Index, tok.Index+1, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := coreconsensus.Entry{Position: coreconsensus.Position{Term: tok.Term, Index: tok.Index + 1}, Kind: 0x80, Data: []byte("x")}
	e.BodySum = wire.BodySum(e.Data)
	e.ChainSum = wire.ChainSum(prev[0].ChainSum, &e)
	m := coreconsensus.Message{
		Version: 1, Group: 1, Kind: coreconsensus.MsgApp, From: l.id, To: f.id, Term: l.node.Status().Term,
		Index: tok.Index, LogTerm: tok.Term, Entries: []coreconsensus.Entry{e},
	}
	f.node.deliver(coreconsensus.AuthenticatedPeer{ID: l.id}, appendFrame(&m))
	deadline := time.Now().Add(5 * time.Second)
	for !f.node.stopped.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the node kept running after an unknown entry kind")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if r, ok := f.node.StopReason(); !ok || !errors.Is(r.Err, coreconsensus.VersionUnsupported) {
		t.Fatalf("stop reason: %+v", r)
	}
}

// An unknown kind already in the log, from a newer binary, stops the node
// when it reaches apply: it is never applied as a no-op (review M3).
func TestUnknownEntryKindInTheLogStopsTheNode(t *testing.T) {
	c := newCluster(t, 1, 0)
	tn := c.nodes[0]
	c.waitApplied(1, 5*time.Second)
	tok := propose(t, tn.node, "a=1")
	c.waitApplied(tok.Index, 5*time.Second)
	c.kill(tn)
	prev, err := tn.log.Entries(tok.Index, tok.Index+1, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := coreconsensus.Entry{Position: coreconsensus.Position{Term: tok.Term, Index: tok.Index + 1}, Kind: 0x80, Data: []byte("x")}
	e.BodySum = wire.BodySum(e.Data)
	e.ChainSum = wire.ChainSum(prev[0].ChainSum, &e)
	hs, _ := tn.log.HardState()
	hs.Commit = e.Position.Index
	if err := tn.log.Append([]coreconsensus.Entry{e}, &hs); err != nil {
		t.Fatal(err)
	}
	if err := tn.log.Sync(); err != nil {
		t.Fatal(err)
	}
	c.start(tn)
	deadline := time.Now().Add(5 * time.Second)
	for !tn.node.stopped.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("applied %d: the unknown entry was skipped", tn.node.Status().Applied)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if r, ok := tn.node.StopReason(); !ok || !errors.Is(r.Err, coreconsensus.VersionUnsupported) {
		t.Fatalf("stop reason: %+v", r)
	}
}

// The tick follows the committed timing contract, not the one the node
// started with: a faster contract keeps heartbeats within its fifth of the
// election timeout (review of PR #301, L5).
func TestTickFollowsTheCommittedContract(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.leader(5 * time.Second)
	propose(t, l.node, "a=1")
	st := l.node.Status()
	next := testTimingFast()
	next.HeartbeatInterval = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pre := coreconsensus.Precondition{Log: l.node.ident, Generation: st.Timing.Generation}
	if err := l.node.ProposeTimingContract(ctx, "op:test", pre, next); err != nil {
		t.Fatal(err)
	}
	want := next.HeartbeatInterval / 5
	deadline := time.Now().Add(5 * time.Second)
	for _, tn := range c.nodes {
		for time.Duration(tn.node.tickEvery.Load()) != want {
			if time.Now().After(deadline) {
				t.Fatalf("node %d ticks every %v, want %v", tn.id, time.Duration(tn.node.tickEvery.Load()), want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}
