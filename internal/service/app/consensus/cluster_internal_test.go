package consensus

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// must curries a fallible test step: must(step())(t) yields the step's value,
// or stops the test when the step failed. Every value it guards is one the
// test goes on to rely on, so continuing would assert on garbage. The curried
// shape exists because Go spreads a multi-value call only when it is the sole
// argument.
func must[T any](v T, err error) func(testing.TB) T {
	return func(t testing.TB) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

// kv is a key-value state machine held in memory: Apply sets "k=v" and
// returns the previous value.
type kv struct {
	mu   sync.Mutex
	m    map[string]string
	last uint64
}

func newKV() *kv { return &kv{m: map[string]string{}} }

func (s *kv) Apply(e coreconsensus.EntryValue) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, v, _ := bytes.Cut(e.Data, []byte("="))
	key := string(k)
	prev := s.m[key]
	s.m[key] = string(v)
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
	keys := slices.Sorted(maps.Keys(s.m))
	for _, k := range keys {
		b = binary.AppendUvarint(b, uint64(len(k)))
		b = append(b, k...)
		b = binary.AppendUvarint(b, uint64(len(s.m[k])))
		b = append(b, s.m[k]...)
	}
	return kvSnap{data: b}, nil
}

func (s *kv) Restore(r io.Reader, _ coreconsensus.SnapshotMetaValue) error {
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
	members []coreconsensus.MemberValue
	every   uint64
	recent  int64
	size    int
	// timing replaces testTimingFast when set, and seed, when not 0, seeds
	// node k's election timeouts with seed+k.
	timing coreconsensus.TimingContractSpec
	seed   uint64
	// configure, when set, adjusts the configuration node tn starts with.
	configure func(tn *testNode, cfg *Config)
}

func testTimingFast() coreconsensus.TimingContractSpec {
	return coreconsensus.TimingContractSpec{
		Generation: 1, HeartbeatInterval: 10 * time.Millisecond,
		ElectionTimeoutMin: 100 * time.Millisecond, ElectionTimeoutMax: 200 * time.Millisecond, MaxClockDrift: 0.05,
	}
}

func (c *cluster) config(tn *testNode) Config {
	timing, seed := testTimingFast(), uint64(0)
	if c.timing.Generation != 0 {
		timing = c.timing
	}
	if c.seed != 0 {
		seed = c.seed + uint64(slices.Index(c.nodes, tn))
	}
	cfg := Config{
		Cluster: c.cid, Machine: tn.sm, Durability: DurabilityVolatile, Log: tn.log, Snapshots: tn.snaps,
		Transport: NewMemoryTransport(c.net, tn.id), InitialTiming: timing, MaxSnapshotBytes: 1 << 30,
		SnapshotEveryBytes: 1 << 20, MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 1,
		SnapshotEvery: c.every, SnapshotTrailing: 4, JoinAuthorizer: allowJoin{}, RecentEntriesBytes: c.recent,
		Seed: seed,
	}
	if c.configure != nil {
		c.configure(tn, &cfg)
	}
	return cfg
}

type allowJoin struct{}

func (allowJoin) Authorize(_ context.Context, r coreconsensus.JoinRequestValue) (coreconsensus.Operator, error) {
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
	for i := range size {
		tn := &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
		id, inc, err := InitLog(tn.log, c.cid, fmt.Sprintf("slot-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		tn.id = id
		c.nodes = append(c.nodes, tn)
		c.members = append(c.members, coreconsensus.MemberValue{
			ID: id, Incarnation: inc, Slot: fmt.Sprintf("slot-%d", i),
			SPKI: MemorySPKI(id), Voter: true,
		})
	}
	for _, tn := range c.nodes {
		if err := Bootstrap(context.Background(), new(c.config(tn)), c.members); err != nil {
			t.Fatal(err)
		}
		// Rerunning with the identical list is an idempotent success.
		if err := Bootstrap(context.Background(), new(c.config(tn)), c.members); err != nil {
			t.Fatal(err)
		}
	}
	for _, tn := range c.nodes {
		c.start(tn)
	}
	t.Cleanup(func() {
		for _, tn := range c.nodes {
			if tn.node != nil {
				if err := tn.node.HardClose(); err != nil {
					t.Errorf("hard close %d: %v", tn.id, err)
				}
			}
		}
	})
	return c
}

func (c *cluster) start(tn *testNode) {
	tn.sm = newKV()
	n, err := NewNode(c.config(tn))
	if err != nil {
		c.t.Fatalf("NewNode %d: %v", tn.id, err)
	}
	tn.node = n
}

func (c *cluster) kill(tn *testNode) {
	c.t.Helper()
	if err := tn.node.HardClose(); err != nil {
		c.t.Fatalf("hard close %d: %v", tn.id, err)
	}
	if err := NewMemoryTransport(c.net, tn.id).Close(); err != nil {
		c.t.Fatalf("close transport %d: %v", tn.id, err)
	}
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

// settledLeader returns a leader whose term already has a committed entry,
// so its term's no-op is behind it: the state a membership change requires.
// Its own proof is an entry proposed and committed in the term it still
// leads; a leadership that moved meanwhile is looked for again.
func (c *cluster) settledLeader(timeout time.Duration) *testNode {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		l := c.leader(timeout)
		tok := propose(c.t, l.node, "settle=1")
		st := l.node.Status()
		if st.Role == coreconsensus.RoleLeader && st.Term == tok.Term && st.Commit >= tok.Index {
			return l
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("no leader committed an entry of its own term (last %d: %+v, token %v)", l.id, st, tok)
		}
		time.Sleep(5 * time.Millisecond)
	}
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

func propose(t *testing.T, n *Node, kv string) coreconsensus.TokenValue {
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

// proposeOnLeader proposes kv to whichever node leads now, again on a new
// leader when the one asked lost its leadership before answering. Only for
// a set the test may write twice: an OUTCOME_UNKNOWN proposal may have
// committed, and retrying it writes the same value again; an
// ENTRY_SUPERSEDED one did not. Under load a two-node majority can change
// leader mid-sequence, and propose, bound to one node, then fails (#304).
func proposeOnLeader(t *testing.T, c *cluster, kv string) coreconsensus.TokenValue {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		l := c.leader(5 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		tok, _, err := l.node.Propose(ctx, []byte(kv))
		cancel()
		if err == nil {
			return tok
		}
		retry := errors.Is(err, coreconsensus.NotLeader) ||
			errs.HasCode(err, coreconsensus.CodeOutcomeUnknown) || errs.HasCode(err, coreconsensus.CodeEntrySuperseded)
		if !retry || time.Now().After(deadline) {
			t.Fatalf("propose %q: %v", kv, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// compactedPast waits until the log of every live node starts past index:
// whichever of them leads when the stopped node comes back, it can only
// catch that node up by an install, which is what the callers exercise.
func compactedPast(t *testing.T, c *cluster, index uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		behind := 0
		for _, tn := range c.nodes {
			if tn.node == nil {
				continue
			}
			if first, _ := tn.log.Bounds(); first <= index {
				behind++
			}
		}
		if behind == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d live nodes never compacted past %d", behind, index)
		}
		time.Sleep(10 * time.Millisecond)
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
	var tok coreconsensus.TokenValue
	for i := range 200 {
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
	if metas := must(lag.snaps.List())(t); len(metas) == 0 {
		t.Fatal("the lagging node caught up without a snapshot")
	}
	// The install compacted the lagging node's log past its end, where the
	// store holds no entry to read the term from: the engine passes it, and
	// the base never answers 0, which a node restarted with no table would
	// vote on (#415).
	if first, _ := lag.log.Bounds(); first > 1 {
		if term, err := lag.log.Term(first - 1); err != nil || term == 0 {
			t.Fatalf("the lagging node's base at %d has term %d (%v), want the snapshot's", first-1, term, err)
		}
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
	st := must(tn.log.Stamp())(t)
	if _, err := NewMemoryTransport(c.net, id).Join(ctx, coreconsensus.JoinConfig{Stamp: st, Address: "mem"}); err != nil {
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
	vm := coreconsensus.MemberValue{ID: victim, SPKI: [32]byte{1}}
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
	// The transfer returned once the old leader SAW the target lead; the
	// target's own Status is published after its own turn, which a loaded
	// scheduler may not have run yet. A fixed grace is a guess about that
	// scheduler, so the condition is waited for, on a bound.
	deadline := time.Now().Add(5 * time.Second)
	for st := to.node.Status(); st.Role != coreconsensus.RoleLeader; st = to.node.Status() {
		if time.Now().After(deadline) {
			t.Fatalf("target role %v (term %d, leader %d)", st.Role, st.Term, st.Leader)
		}
		time.Sleep(5 * time.Millisecond)
	}
	propose(t, to.node, "b=2")
}

func TestStorageFailureStopsTheNode(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.leader(5 * time.Second)
	l.log.FailSync(errors.New("injected fdatasync failure"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// This proposal only provokes the sync; whether it is refused or
	// acknowledged by the followers' majority is not what the test pins —
	// the stop below is — so its outcome is recorded rather than asserted.
	if _, _, err := l.node.Propose(ctx, []byte("a=1")); err != nil {
		t.Logf("proposal under a failing sync: %v", err)
	}
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
	m := &coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgTimeoutNow, From: l.id, To: f.id, Term: l.node.Status().Term}
	frame := appendFrame(m)
	var liar coreconsensus.NodeID
	for _, tn := range c.nodes {
		if tn != l && tn != f {
			liar = tn.id
		}
	}
	f.node.deliver(coreconsensus.AuthenticatedPeerValue{ID: liar}, frame)
	if got := f.node.Health().DroppedFrames; got != before+1 {
		t.Fatalf("dropped %d, want %d", got, before+1)
	}
	time.Sleep(100 * time.Millisecond)
	if f.node.Status().Role == coreconsensus.RoleLeader {
		t.Fatal("a forged TimeoutNow was obeyed")
	}
}

func appendFrame(m *coreconsensus.MessageValue) []byte { return wire.AppendMessage(nil, m) }

// A transfer submitted on a follower succeeds, or fails with TRANSFER_FAILED
// and the old leader resumes; never OUTCOME_UNKNOWN (D8). NOT_LEADER is the
// other answer the design allows: a transfer refuses new proposals from the
// moment it starts, and nothing was appended, so it is retried (ADR 0152,
// "A transfer loses nothing in flight"). Every round must still end with the
// leadership on its target, within a bounded time.
func TestClusterTransferSubmittedOnAFollower(t *testing.T) {
	c := newCluster(t, 3, 0)
	for round := range 10 {
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

// leaderHealth polls the verdict of whichever node leads now until ok holds
// on it, and fails the test after five seconds. The verdict asked is a
// leader's — only a leader reports peers and a failure tolerance — and the
// leadership may move meanwhile: CheckQuorum steps a leader down after one
// minimum election timeout (100 ms here) without a majority's answer, and
// with one voter of three isolated a single answer is left, so one stall of
// a slow runner (Windows, -race) re-elects. Reading the verdict on a node
// fixed beforehand then reads a follower's: no peers, tolerance -1.
func leaderHealth(t *testing.T, c *cluster, what string, ok func(l *testNode, h HealthValue) bool) *testNode {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		l := c.leader(5 * time.Second)
		h := l.node.Health()
		if h.Status.Role == coreconsensus.RoleLeader && ok(l, h) {
			return l
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: leader %d: %+v", what, l.id, h)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// otherThan returns a node of c that is not n.
func (c *cluster) otherThan(n *testNode) *testNode {
	for _, tn := range c.nodes {
		if tn != n {
			return tn
		}
	}
	c.t.Fatal("no other node")
	return nil
}

// isolatedPeerLags reports whether h, a leader's verdict, counts the
// isolated node f unreachable and behind, with no failure left to tolerate.
func isolatedPeerLags(f *testNode, h HealthValue) bool {
	for _, p := range h.Peers {
		if p.ID == f.id {
			return h.FailureTolerance == 0 && p.Flags.Has(PeerUnreachable) && p.Lag > 0
		}
	}
	return false
}

func TestHealthReportsFailureToleranceAndPeers(t *testing.T) {
	c := newCluster(t, 3, 0)
	tok := proposeOnLeader(t, c, "a=1")
	c.waitApplied(tok.Index, 5*time.Second)
	threeVoters := func(_ *testNode, h HealthValue) bool {
		return h.FailureTolerance == 1 && h.Flags.Has(HealthQuorumReachable) && len(h.Peers) == 2 &&
			!h.Peers[0].Flags.Has(PeerUnreachable) && !h.Peers[1].Flags.Has(PeerUnreachable) && h.Peers[0].Lag == 0
	}
	// A follower's verdict is judged against the leadership it follows: when
	// the leader read has kept its term and f follows it in that term, the
	// verdict must be a follower's at once; a leadership that moved between
	// the two reads is looked for again, within the same bound.
	deadline := time.Now().Add(5 * time.Second)
	var f *testNode
	for {
		l := leaderHealth(t, c, "tolerance 1 with three voters", threeVoters)
		f = c.otherThan(l)
		h := f.node.Health()
		ls := l.node.Status()
		if ls.Role == coreconsensus.RoleLeader && h.Status.Role == coreconsensus.RoleFollower &&
			h.Status.Term == ls.Term && h.Status.Leader == l.id {
			if h.FailureTolerance != -1 || h.Peers != nil || !h.Flags.Has(HealthHasLeader) || !h.Flags.Has(HealthReady) {
				t.Fatalf("follower health %+v", h)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no stable leadership to judge a follower against: leader %d %+v, follower %+v", l.id, ls, h)
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.net.Isolate(f.id, true)
	defer c.net.Isolate(f.id, false)
	proposeOnLeader(t, c, "b=2")
	leaderHealth(t, c, "the isolated peer is unreachable and lags", func(l *testNode, h HealthValue) bool {
		if l == f {
			t.Fatalf("the isolated node %d leads: %+v", f.id, h)
		}
		return isolatedPeerLags(f, h)
	})
}

// The verdict follows the leadership: a leader elected while a voter is
// isolated reports that voter unreachable and lagging from its first turns,
// and the leader it replaced reports a follower's verdict. The transfer
// stands in for the CheckQuorum step-down a slow runner produces, which the
// verdict read on the old leader mistook for a wrong health (the Windows
// flake of TestHealthReportsFailureToleranceAndPeers).
func TestHealthFollowsALeadershipChange(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	c.net.Isolate(f.id, true)
	defer c.net.Isolate(f.id, false)
	deadline := time.Now().Add(10 * time.Second)
	var old *testNode
	for {
		old = c.leader(5 * time.Second)
		var to *testNode
		for _, tn := range c.nodes {
			if tn != old && tn != f {
				to = tn
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := old.node.TransferLeadership(ctx, "op:test", to.id)
		cancel()
		if err == nil {
			break
		}
		if !errors.Is(err, coreconsensus.TransferFailed) && !errors.Is(err, coreconsensus.NotLeader) {
			t.Fatalf("transfer to %d: %v", to.id, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("no transfer completed, last: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	nl := leaderHealth(t, c, "the new leader counts the isolated peer", func(l *testNode, h HealthValue) bool {
		return l != old && l != f && isolatedPeerLags(f, h)
	})
	settle := time.Now().Add(5 * time.Second)
	for {
		h := old.node.Health()
		if h.Status.Role == coreconsensus.RoleFollower && h.Status.Leader == nl.id {
			if h.FailureTolerance != -1 || h.Peers != nil {
				t.Fatalf("the replaced leader still reports a leader's verdict: %+v", h)
			}
			return
		}
		if time.Now().After(settle) {
			t.Fatalf("the replaced leader never followed %d: %+v", nl.id, h)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// R7-18: every snapshot of a node fails its checksum. It starts with no
// table, never campaigns, asks for an install and catches up through it.
func TestClusterNodeWithoutAValidSnapshotAsksForAnInstall(t *testing.T) {
	c := newCluster(t, 3, 20)
	l := c.leader(5 * time.Second)
	var tok coreconsensus.TokenValue
	for i := range 120 {
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
		metas := must(f.snaps.List())(t)
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
	var tok coreconsensus.TokenValue
	for i := range 300 {
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
		if err := tn.node.HardClose(); err != nil {
			t.Fatalf("hard close %d: %v", tn.id, err)
		}
	}
	for _, tn := range c.nodes {
		if err := NewMemoryTransport(c.net, tn.id).Close(); err != nil {
			t.Fatalf("close transport %d: %v", tn.id, err)
		}
		tn.sm = newKV()
		cfg := c.config(tn)
		cfg.Reads = ReadLease
		// A 50 ms budget (1 s × 0.05): ordinary turns under -race stay
		// below it, the 200 ms pause below does not.
		cfg.InitialTiming = coreconsensus.TimingContractSpec{
			Generation: 1, HeartbeatInterval: 50 * time.Millisecond,
			ElectionTimeoutMin: time.Second, ElectionTimeoutMax: 2 * time.Second, MaxClockDrift: 0.05,
		}
		n, err := NewNode(cfg)
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
	// A JOIN is a membership change, and a leader admits none before its
	// term's no-op committed (MEMBERSHIP_CHANGE_PENDING): taking the leader
	// by role alone races that commit.
	l := c.settledLeader(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peer := coreconsensus.AuthenticatedPeerValue{Slot: "burst", SPKI: [32]byte{7}}
	req := coreconsensus.JoinRequestValue{Cluster: c.cid, ID: 991, Slot: "burst"}
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

func (g gatedKV) Apply(e coreconsensus.EntryValue) any {
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
		if err := tn.node.HardClose(); err != nil {
			t.Fatalf("hard close %d: %v", tn.id, err)
		}
		if err := NewMemoryTransport(c.net, tn.id).Close(); err != nil {
			t.Fatalf("close transport %d: %v", tn.id, err)
		}
	}
	for _, tn := range c.nodes {
		tn.sm = newKV()
		cfg := c.config(tn)
		cfg.Machine = gatedKV{kv: tn.sm, gate: gate}
		cfg.MaxApplyLagBytes = 2048
		n, err := NewNode(cfg)
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

// batchKV is a kv that applies a run of commands in one ApplyBatch.
type batchKV struct{ *kv }

func (b batchKV) ApplyBatch(es []coreconsensus.EntryValue, results []any) {
	for i := range es {
		results[i] = b.Apply(es[i])
	}
}

// #507: a node refuses a snapshot offer of another history, LINEAGE_MISMATCH,
// before a byte is spooled: the entries after it would be applied under an
// identity it does not continue.
func TestAnOfferOfAnotherLineageIsRefused(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	st := f.node.Status()
	offerOf := func(log coreconsensus.LogIdentityValue) coreconsensus.InstallAcceptValue {
		id := fmt.Sprintf("%020d-%020d", st.Commit+100, st.Term)
		return f.node.offer(coreconsensus.AuthenticatedPeerValue{ID: l.id}, coreconsensus.InstallOfferValue{
			Term: st.Term, SnapshotID: id,
			Meta: coreconsensus.SnapshotMetaValue{
				ID: id, Version: 1, Log: log,
				Position: coreconsensus.PositionValue{Term: st.Term, Index: st.Commit + 100},
			},
		})
	}
	if acc := offerOf(coreconsensus.LogIdentityValue{Cluster: coreconsensus.ClusterID{9}, Epoch: 1}); acc.Refused != coreconsensus.CodeLineageMismatch {
		t.Fatalf("an offer of another cluster: %+v, want LINEAGE_MISMATCH", acc)
	}
	if acc := offerOf(f.node.lineage()); acc.Refused == coreconsensus.CodeLineageMismatch {
		t.Fatalf("an offer of the node's own lineage was refused: %+v", acc)
	}
}

// proposeThroughLag proposes kv on the leader, again while the apply lag
// refuses it: a lag that drains is back-pressure, one that never drains
// fails the test once the deadline passes, with the leader's lag.
func proposeThroughLag(t *testing.T, c *cluster, kv string) coreconsensus.TokenValue {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		l := c.leader(5 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		tok, _, err := l.node.Propose(ctx, []byte(kv))
		cancel()
		if err == nil {
			return tok
		}
		if !errors.Is(err, coreconsensus.Overloaded) && !errors.Is(err, coreconsensus.NotLeader) {
			t.Fatalf("propose %q: %v", kv, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("propose %q still refused after 10 s: %v; leader applyLag %d", kv, err, l.node.applyLag.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// #514: with a BatchingStateMachine, every entry of a batch releases its
// bytes from the apply lag, not only the first one. Once everything is
// applied the lag is zero on every node, and the leader accepts proposals.
func TestBatchingMachineReleasesApplyLag(t *testing.T) {
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 3},
		configure: func(tn *testNode, cfg *Config) {
			cfg.Machine = batchKV{kv: tn.sm}
			cfg.MaxApplyLagBytes = 64 << 10
		},
	})
	l := c.leader(5 * time.Second)
	payload := strings.Repeat("x", 1024)
	var wg sync.WaitGroup
	for w := range 16 {
		wg.Go(func() {
			for i := range 16 {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				// A refusal or a leadership change during the load is not
				// the subject: the lag once everything is applied is.
				_, _, _ = l.node.Propose(ctx, fmt.Appendf(nil, "k%d-%d=%s", w, i, payload))
				cancel()
			}
		})
	}
	wg.Wait()
	tok := proposeThroughLag(t, c, "last=1")
	c.waitApplied(tok.Index, 10*time.Second)
	for _, tn := range c.nodes {
		if got := tn.node.applyLag.Load(); got != 0 {
			t.Errorf("node %d: applyLag %d bytes after everything applied, want 0", tn.id, got)
		}
	}
	// Bytes several times MaxApplyLagBytes went through: a leak would now
	// refuse every proposal.
	for i := range 20 {
		proposeThroughLag(t, c, fmt.Sprintf("after%d=%s", i, payload))
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
	// Isolated before it stops: the memory network keeps what is sent to a
	// stopped node and delivers it at the restart, so the appends the leader
	// went on sending caught the node up past the live logs' start whenever
	// they were compacted no further, and no install was ever needed (#374,
	// linux/386). The crash is armed before the leader can reach the
	// restarted node: armed after, an install that published in between left
	// nothing to crash and the wait below timed out (#304, macOS).
	c.net.Isolate(lag.id, true)
	c.kill(lag)
	var tok coreconsensus.TokenValue
	for i := range 200 {
		tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%d", i, i))
	}
	compactedPast(t, c, 10)
	c.start(lag)
	lag.node.crashAfterPublish.Store(true)
	c.net.Isolate(lag.id, false)
	deadline := time.Now().Add(10 * time.Second)
	for !lag.node.stopped.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("no install reached publication: the node applied %d", lag.node.Status().Applied)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if metas := must(lag.snaps.List())(t); len(metas) == 0 {
		t.Fatal("nothing was published before the crash")
	}
	c.kill(lag)
	c.start(lag)
	c.waitApplied(tok.Index, 10*time.Second)
	if v, _ := lag.sm.get("k199"); v != "199" {
		t.Fatalf("after resuming the install: k199=%q", v)
	}
}

// An install onto a node that already holds two snapshots of its own keeps
// two, as a cut does (D7, R6-02): it used to leave three until that node's
// next cut, which TestDiskClusterSurvivesFullRestart caught under load (#304).
func TestAnInstallKeepsTheTwoNewestSnapshots(t *testing.T) {
	c := newCluster(t, 3, 20)
	l := c.leader(5 * time.Second)
	var lag *testNode
	for _, tn := range c.nodes {
		if tn != l {
			lag = tn
			break
		}
	}
	var tok coreconsensus.TokenValue
	for i := range 100 {
		tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%d", i, i))
	}
	c.waitApplied(tok.Index, 10*time.Second)
	// The second cut may be one a trigger coalesced while the first was
	// written, made once it was: it used to be dropped, and a follower that
	// had applied all hundred entries behind its first cut never cut again
	// (#375, windows and macOS; TestACoalescedCutIsMade).
	deadline := time.Now().Add(10 * time.Second)
	for len(must(lag.snaps.List())(t)) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the follower never cut two snapshots of its own")
		}
		time.Sleep(5 * time.Millisecond)
	}
	held := tok.Index
	// Isolated while stopped, so that what the leader sends meanwhile is not
	// kept for its restart: past the live logs' start, only an install can
	// catch it up (see TestInstallResumesAfterACrashAtPublication).
	c.net.Isolate(lag.id, true)
	c.kill(lag)
	for i := 100; i < 300; i++ {
		tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%d", i, i))
	}
	compactedPast(t, c, held+1)
	c.start(lag)
	c.net.Isolate(lag.id, false)
	c.waitApplied(tok.Index, 10*time.Second)
	// The applied index moves with the restore, a moment before the install
	// prunes: the count is waited for, on a bound.
	deadline = time.Now().Add(5 * time.Second)
	metas := must(lag.snaps.List())(t)
	for len(metas) > 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		metas = must(lag.snaps.List())(t)
	}
	// Exactly two: the installed one and the newest it held. One would be a
	// retention that removed a snapshot too many.
	if len(metas) != 2 {
		t.Fatalf("after the install the follower holds %d snapshots, want 2", len(metas))
	}
	if metas[0].Position.Index <= held {
		t.Fatalf("the follower's newest snapshot is at %d, not past the %d it held: no install", metas[0].Position.Index, held)
	}
}

// staleSnapshot runs a cluster until a follower applied past the leader's
// newest snapshot, and returns that snapshot's meta and encoded body.
func staleSnapshot(t *testing.T) (c *cluster, l, f *testNode, meta coreconsensus.SnapshotMetaValue, body []byte, tok coreconsensus.TokenValue) {
	c = newCluster(t, 3, 20)
	l = c.leader(5 * time.Second)
	for _, tn := range c.nodes {
		if tn != l {
			f = tn
			break
		}
	}
	for i := range 60 {
		propose(t, l.node, fmt.Sprintf("k%d=%d", i, i))
	}
	c.waitApplied(l.node.Status().Commit, 5*time.Second)
	metas := leaderSnapshots(t, l)
	meta = metas[0]
	h, err := l.snaps.Open(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	body = make([]byte, meta.EncodedSize)
	if _, err := h.ReadAt(body, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	h.Close()
	// Entries past the snapshot that the receiver applies before the body
	// arrives.
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
	for i := range 80 {
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
	err = f.node.install(coreconsensus.InstallParams{
		Ctx:   context.Background(),
		From:  coreconsensus.AuthenticatedPeerValue{ID: l.id},
		Offer: coreconsensus.InstallOfferValue{Term: l.node.Status().Term, SnapshotID: meta.ID, Meta: meta},
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
	if _, err := NewNode(cfg); !errors.Is(err, coreconsensus.ConsensusMisconfigured) {
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
	e := coreconsensus.EntryValue{Position: coreconsensus.PositionValue{Term: tok.Term, Index: tok.Index + 1}, Kind: 0x80, Data: []byte("x")}
	e.BodySum = wire.BodySum(e.Data)
	e.ChainSum = wire.ChainSum(prev[0].ChainSum, &e)
	m := &coreconsensus.MessageValue{
		Version: 1, Group: 1, Kind: coreconsensus.MsgApp, From: l.id, To: f.id, Term: l.node.Status().Term,
		Index: tok.Index, LogTerm: tok.Term, Entries: []coreconsensus.EntryValue{e},
	}
	f.node.deliver(coreconsensus.AuthenticatedPeerValue{ID: l.id}, appendFrame(m))
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
	e := coreconsensus.EntryValue{Position: coreconsensus.PositionValue{Term: tok.Term, Index: tok.Index + 1}, Kind: 0x80, Data: []byte("x")}
	e.BodySum = wire.BodySum(e.Data)
	e.ChainSum = wire.ChainSum(prev[0].ChainSum, &e)
	hs, err := tn.log.HardState()
	if err != nil {
		t.Fatal(err)
	}
	hs.Commit = e.Position.Index
	if err := tn.log.Append([]coreconsensus.EntryValue{e}, &hs); err != nil {
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
	pre := coreconsensus.PreconditionSpec{Log: l.node.lineage(), Generation: st.Timing.Generation}
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

// A deposed leader keeps the proposals it appended and never committed. When
// a snapshot installed over them moves applied past their index, applied1
// never runs for them and sweepWaiters answers only above the commit: their
// callers waited forever and their bytes stayed counted until every
// proposal was refused OVERLOADED. The restore answers them OUTCOME_UNKNOWN
// and releases their bytes (#419).
// leaderSnapshots waits until the leader has published a snapshot, and
// returns its snapshots. propose returns once an entry is applied, but a cut
// is written on a goroutine of its own (maybeSnapshot, cut): reading the list
// right after the last proposal found it empty about once in 200 runs (#427).
func leaderSnapshots(t *testing.T, l *testNode) []coreconsensus.SnapshotMetaValue {
	t.Helper()
	var metas []coreconsensus.SnapshotMetaValue
	waitFor(t, "a leader snapshot", func() bool {
		var err error
		metas, err = l.snaps.List()
		if err != nil {
			t.Fatalf("list the leader's snapshots: %v", err)
		}
		return len(metas) > 0
	})
	return metas
}

func TestARestoredSnapshotAnswersTheProposalsItCovers(t *testing.T) {
	c := newCluster(t, 3, 20)
	l := c.leader(5 * time.Second)
	f := c.otherThan(l)
	c.net.Isolate(f.id, true)
	for i := range 60 {
		propose(t, l.node, fmt.Sprintf("k%d=%d", i, i))
	}
	meta := leaderSnapshots(t, l)[0]
	h, err := l.snaps.Open(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, meta.EncodedSize)
	if _, err := h.ReadAt(body, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	h.Close()
	f.node.am.Lock()
	applied := f.node.applied
	f.node.am.Unlock()
	if applied+3 > meta.Position.Index {
		t.Fatalf("setup: the follower applied %d, the snapshot is at %d", applied, meta.Position.Index)
	}
	// Proposals a deposed leader appended below the snapshot, and one above.
	var ps []*proposal
	f.node.am.Lock()
	for _, idx := range []uint64{applied + 1, applied + 2, meta.Position.Index, meta.Position.Index + 1} {
		p := &proposal{entry: coreconsensus.EntryValue{Data: []byte("orphan")}, done: make(chan propResult, 1), local: true}
		p.pos = coreconsensus.PositionValue{Term: 1, Index: idx}
		p.stage.Store(stageAppended)
		f.node.waiters[idx] = p
		f.node.pendingBytes.Add(int64(len(p.entry.Data)))
		ps = append(ps, p)
	}
	f.node.am.Unlock()
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
	for _, p := range ps[:3] {
		select {
		case r := <-p.done:
			if !errors.Is(r.err, coreconsensus.OutcomeUnknown) {
				t.Fatalf("waiter at %d answered %v, want OUTCOME_UNKNOWN", p.pos.Index, r.err)
			}
		default:
			t.Fatalf("waiter at %d, covered by the snapshot at %d, was never answered", p.pos.Index, meta.Position.Index)
		}
	}
	f.node.am.Lock()
	_, kept := f.node.waiters[meta.Position.Index+1]
	left := len(f.node.waiters)
	f.node.am.Unlock()
	if !kept || left != 1 {
		t.Fatalf("%d waiter(s) left, want only the one above the snapshot", left)
	}
	if got := f.node.pendingBytes.Load(); got != int64(len("orphan")) {
		t.Fatalf("pending bytes %d, want only the waiter above the snapshot's", got)
	}
	f.node.am.Lock()
	delete(f.node.waiters, meta.Position.Index+1)
	f.node.am.Unlock()
	f.node.pendingBytes.Add(-int64(len("orphan")))
}

// A Propose that passed the stopped check just before a stop finds propc
// and stopc both ready, and Go picks one at random. When it picked propc
// after failAllOnStop drained it for the last time, nothing answered the
// proposal: the caller waited its whole deadline, forever without one. It
// is answered NODE_STOPPED once the loop is gone (#420).
func TestAProposeThatRacesAStopIsAnswered(t *testing.T) {
	c := newCluster(t, 1, 0)
	l := c.leader(5 * time.Second)
	n := l.node
	c.kill(l)
	<-n.loopDone
	// The race, made certain: the stopped check passes, then the select
	// finds propc's buffer free and stopc closed.
	n.stopped.Store(false)
	defer n.stopped.Store(true)
	for i := range 64 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		start := time.Now()
		_, _, err := n.Propose(ctx, []byte("a=1"))
		cancel()
		if !errors.Is(err, coreconsensus.NodeStopped) || time.Since(start) > time.Second {
			t.Fatalf("Propose %d on a stopped node: %v after %v, want NODE_STOPPED at once", i, err, time.Since(start))
		}
	}
}
