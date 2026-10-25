package consensus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// A voter that joined announcing a MaxSnapshotBytes kept that limit in the
// membership for good: nothing recorded a raised one. Once the state
// outgrew it, raising MaxSnapshotBytes on every node and restarting the
// voter (the remedy #512 names) changed nothing: every cut of every node was
// still held to the old limit and failed SNAPSHOT_TOO_LARGE, no log was
// compacted, and the no-space alarm refused every write of the cluster
// (#677).
func TestARaisedSnapshotLimitReachesTheMembership(t *testing.T) {
	raisedLimitRun(t, 4096)
}

// The control: the same cluster, the joiner announcing no limit. The cuts
// follow the nodes' own 1 GiB, the logs are compacted, and no write is
// refused.
func TestWithNoAnnouncedLimitTheCutsFollowTheNodesOwn(t *testing.T) {
	raisedLimitRun(t, 0)
}

func raisedLimitRun(t *testing.T, announced int64) {
	var joinerID coreconsensus.NodeID
	joinerLimit := max(announced, 4096)
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{7, 7, 0x5d}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		if joinerID != 0 && tn.id == joinerID {
			cfg.MaxSnapshotBytes, cfg.SnapshotEveryBytes = joinerLimit, 1024
			return
		}
		// The founders: a log quota of 300 KiB less a reserve of 100 KiB,
		// read as 1 KiB per entry held; every MaxSnapshotBytes is 1 GiB.
		cfg.Log = quotaLog{MemoryLog: tn.log}
		cfg.MaxLogBytes, cfg.LogReserveBytes = 300<<10, 100<<10
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	for i := range 10 {
		propose(t, l.node, fmt.Sprintf("k%03d=v", i))
	}
	tn := newJoiner(t, c, "slot-joiner")
	joinerID = tn.id
	joinAnnouncing(t, c, tn, announced)
	id := tn.id
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		err := c.leader(5*time.Second).node.Promote(ctx, "op:test", id)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("promote: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The operator raises the joiner's limit to the founders' 1 GiB and
	// restarts it.
	c.kill(tn)
	joinerLimit = 1 << 30
	c.start(tn)
	waitFor(t, "the restarted joiner caught up", func() bool {
		return tn.node.Status().Applied >= c.leader(5*time.Second).node.Status().Applied
	})
	if announced > 0 {
		// The joiner states its raised limit: the writes start once the
		// membership records it, or five seconds on without it.
		for deadline := time.Now().Add(5 * time.Second); recordedFor(c, id) != 1<<30 && time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
		}
	}
	l = c.leader(5 * time.Second)
	firstBefore, _ := l.log.Bounds()
	big := strings.Repeat("x", 100)
	var refused error
	writes := 0
	for i := 0; i < 600 && refused == nil; i++ {
		wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
		lead := c.leader(5 * time.Second)
		_, _, err := lead.node.Propose(wctx, fmt.Appendf(nil, "big%04d=%s", i, big))
		wcancel()
		if errors.Is(err, coreconsensus.NoSpace) {
			refused = err
		}
		writes++
	}
	l = c.leader(5 * time.Second)
	m, _ := l.node.Membership().Member(id)
	lf, ll := l.log.Bounds()
	t.Logf("joiner's MaxSnapshotBytes now %d, recorded in the membership %d; leader's own %d, Health.SnapshotLimit %d",
		tn.node.cfg.MaxSnapshotBytes, m.MaxSnapshotBytes, l.node.cfg.MaxSnapshotBytes, l.node.Health().SnapshotLimit)
	t.Logf("leader log [%d,%d] (first %d before the writes), newest snapshot %d bytes, %d writes, NoSpace alarm %d",
		lf, ll, firstBefore, newestBytes(l.node), writes, l.node.Status().NoSpace)
	if !m.Voter {
		t.Fatal("setup: the joiner is not a voter")
	}
	if announced > 0 && m.MaxSnapshotBytes != tn.node.cfg.MaxSnapshotBytes {
		t.Errorf("the membership still holds the joiner to %d bytes, though it runs with %d", m.MaxSnapshotBytes, tn.node.cfg.MaxSnapshotBytes)
	}
	if refused != nil {
		t.Errorf("every node runs with MaxSnapshotBytes of 1 GiB and the state is a few dozen KiB, yet the cluster refused a write: %v", refused)
	}
}

// newJoiner is a node prepared for c, not started.
func newJoiner(t *testing.T, c *cluster, slot string) *testNode {
	t.Helper()
	tn := &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
	id, _, err := InitLog(tn.log, c.cid, slot)
	if err != nil {
		t.Fatal(err)
	}
	tn.id = id
	return tn
}

// joinAnnouncing starts tn and has it join c announcing limit, until a
// leader admits it as a learner and it caught up.
func joinAnnouncing(t *testing.T, c *cluster, tn *testNode, limit int64) {
	t.Helper()
	c.nodes = append(c.nodes, tn)
	c.start(tn)
	st := must(tn.log.Stamp())(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, err := NewMemoryTransport(c.net, tn.id).Join(ctx, coreconsensus.JoinConfig{Stamp: st, Address: "mem", MaxSnapshotBytes: limit})
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("join: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	waitFor(t, "the joiner caught up", func() bool {
		return tn.node.Status().Applied >= c.leader(5*time.Second).node.Status().Applied
	})
}

// recordedFor is the MaxSnapshotBytes the leader's membership records for
// id.
func recordedFor(c *cluster, id coreconsensus.NodeID) int64 {
	m, _ := c.leader(5 * time.Second).node.Membership().Member(id)
	return m.MaxSnapshotBytes
}

// tapTransport is a node's end of a memory network that records the
// largest snapshot offered to its node, and makes its node, while old names
// it, a leader of the release before #677 (asPredating).
type tapTransport struct {
	*MemoryTransport
	id      coreconsensus.NodeID
	old     *atomic.Uint64
	offered *atomic.Int64
}

// Bind implements Transport over the tapped callbacks.
func (t *tapTransport) Bind(group coreconsensus.GroupID, deliver func(coreconsensus.AuthenticatedPeerValue, []byte),
	offer func(coreconsensus.AuthenticatedPeerValue, coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue,
	install func(coreconsensus.InstallParams) error,
	join func(context.Context, coreconsensus.AuthenticatedPeerValue, coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code),
) error {
	tapped := func(from coreconsensus.AuthenticatedPeerValue, frame []byte) {
		if t.old != nil && t.old.Load() == uint64(t.id) {
			frame = asPredating(frame)
		}
		deliver(from, frame)
	}
	judged := func(from coreconsensus.AuthenticatedPeerValue, o coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue {
		if t.offered != nil {
			for size := snapshotSize(&o.Meta); ; {
				cur := t.offered.Load()
				if size <= cur || t.offered.CompareAndSwap(cur, size) {
					break
				}
			}
		}
		return offer(from, o)
	}
	return t.MemoryTransport.Bind(group, tapped, judged, install, join)
}

// asPredating is frame as a leader of the release before #677 reads it. That
// release knows no ChangeSnapshotLimit, and its nextConf refuses one
// MEMBERSHIP_INVALID in its default branch: the forwarded change is handed
// to the node as a change kind no release knows, which this release's
// nextConf refuses in the same branch. Every other frame is left as it is.
func asPredating(frame []byte) []byte {
	var m coreconsensus.MessageValue
	if wire.ParseMessage(bytes.Clone(frame), &m) != nil || m.Kind != coreconsensus.MsgForwardAdmin ||
		len(m.Entries) != 1 || m.Entries[0].Kind != coreconsensus.EntryMembership {
		return frame
	}
	e := &m.Entries[0]
	a, err := wire.ParseAdminAt(e.Version, e.Data)
	if err != nil || a.Change != wire.ChangeSnapshotLimit {
		return frame
	}
	a.Change = 0xff
	e.Data = wire.AppendAdminAt(nil, e.Version, &a)
	e.BodySum = wire.BodySum(e.Data)
	return wire.AppendMessage(nil, &m)
}

// warned reports whether l holds a Warn line msg whose fields hold every
// key and value of kv, a value matched when the field contains it.
func warned(l *attrLogger, msg string, kv ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if line["msg"] != msg {
			continue
		}
		ok := true
		for i := 0; i+1 < len(kv); i += 2 {
			ok = ok && strings.Contains(line[kv[i]], kv[i+1])
		}
		if ok {
			return true
		}
	}
	return false
}

const (
	msgAdminOperation = "consensus: administrative operation"
	msgLimitBelow     = "consensus: this node's MaxSnapshotBytes is below its newest snapshot; the lowering is not proposed, and the membership keeps the limit it records"
)

// limitRun is a cluster of three founders at 1 GiB, and a learner that
// joined announcing the MaxSnapshotBytes it runs with, 2048, whose
// transport records the largest snapshot offered to it and whose Warn
// lines are kept.
type limitRun struct {
	c       *cluster
	tn      *testNode
	limit   atomic.Int64
	offered atomic.Int64
	logs    attrLogger
}

func newLimitRun(t *testing.T, cid coreconsensus.ClusterID) *limitRun {
	t.Helper()
	r := &limitRun{}
	r.limit.Store(2048)
	var learner atomic.Uint64
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: cid, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		if uint64(tn.id) != learner.Load() {
			return
		}
		cfg.MaxSnapshotBytes = r.limit.Load()
		cfg.SnapshotEveryBytes = min(cfg.MaxSnapshotBytes, 1024)
		cfg.Logger = &r.logs
		cfg.Transport = &tapTransport{MemoryTransport: NewMemoryTransport(c.net, tn.id), id: tn.id, offered: &r.offered}
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	for i := range 10 {
		propose(t, l.node, fmt.Sprintf("k%03d=v", i))
	}
	r.c, r.tn = c, newJoiner(t, c, "slot-learner")
	learner.Store(uint64(r.tn.id))
	joinAnnouncing(t, c, r.tn, 2048)
	return r
}

// outgrow writes until the leader's newest snapshot is over the learner's
// 2048 bytes and its log starts past the learner's: a snapshot is all that
// can bring the learner back, which the leader withholds (#591), and it
// feeds the learner no entries (#613).
func (r *limitRun) outgrow(t *testing.T) {
	t.Helper()
	value := strings.Repeat("v", 20)
	for i := 0; ; i++ {
		l := r.c.leader(5 * time.Second)
		first, _ := l.log.Bounds()
		_, last := r.tn.log.Bounds()
		if newestBytes(l.node) > 2048 && first > last+1 {
			return
		}
		if i == 2000 {
			t.Fatalf("setup: after %d writes the leader's newest snapshot is %d bytes, its log starts at %d, the learner's ends at %d",
				i, newestBytes(l.node), first, last)
		}
		proposeOnLeader(t, r.c, fmt.Sprintf("w%04d=%s", i, value))
	}
}

// A learner whose limit really is low, 2048 bytes, and which the state
// outgrew, is neither offered nor installs a snapshot over it, and nothing
// raises the limit the membership records for it: only the learner states
// its own (#677). Its operator lowers it further, below its newest
// snapshot, and restarts it: the lowering is not proposed but said, and the
// membership keeps 2048, as Promote refuses a learner the state does not
// fit in (#591). Recorded, a voter's would make every cut of every node
// fail SNAPSHOT_TOO_LARGE.
func TestALowLimitIsNeverOfferedMoreAndItsLoweringBelowTheStateIsNotProposed(t *testing.T) {
	r := newLimitRun(t, coreconsensus.ClusterID{6, 7, 7, 1})
	r.outgrow(t)
	if got := recordedFor(r.c, r.tn.id); got != 2048 {
		t.Fatalf("the membership records %d for the learner, which states 2048", got)
	}
	if o := r.offered.Load(); o > 2048 {
		t.Fatalf("the learner was offered a snapshot of %d bytes, over its limit of 2048", o)
	}
	snaps := must(r.tn.snaps.List())(t)
	if len(snaps) == 0 {
		t.Fatal("setup: the learner holds no snapshot")
	}
	smallest := int64(1 << 62)
	for i := range snaps {
		size := snapshotSize(&snaps[i])
		if size > 2048 {
			t.Fatalf("the learner holds a snapshot of %d bytes, over its limit of 2048", size)
		}
		smallest = min(smallest, size)
	}
	// The operator lowers the learner's limit below every snapshot it holds,
	// and restarts it.
	low := smallest / 2
	r.c.kill(r.tn)
	r.limit.Store(low)
	r.offered.Store(0)
	r.c.start(r.tn)
	proposed := func() bool { return warned(&r.logs, msgAdminOperation, "change", fmt.Sprint(wire.ChangeSnapshotLimit)) }
	waitFor(t, "the lowering below the newest snapshot said or proposed", func() bool {
		return proposed() || warned(&r.logs, msgLimitBelow, "limit", fmt.Sprint(low), "recorded", "2048")
	})
	if proposed() {
		t.Error("the lowering below the newest snapshot was proposed")
	}
	if got := recordedFor(r.c, r.tn.id); got != 2048 {
		t.Errorf("the membership records %d for the learner, want the 2048 it keeps", got)
	}
	if o := r.offered.Load(); o > low {
		t.Errorf("the learner was offered a snapshot of %d bytes, over its limit of %d", o, low)
	}
}

// The learner of #613, its state outgrown, raised to the founders' 1 GiB
// and restarted, states its limit: once the membership records it, the
// leader feeds it again and offers it the snapshot it withheld, the learner
// installs it, and Promote, which refused it while the state did not fit
// (#591), makes it a voter (#677).
func TestALearnerWhoseLimitIsRaisedIsFedInstalledAndPromoted(t *testing.T) {
	r := newLimitRun(t, coreconsensus.ClusterID{6, 7, 7, 2})
	r.outgrow(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.c.leader(5*time.Second).node.Promote(ctx, "op:test", r.tn.id); !errors.Is(err, coreconsensus.MembershipInvalid) {
		t.Fatalf("setup: promoting the learner the state outgrew was %v, want MEMBERSHIP_INVALID", err)
	}
	r.c.kill(r.tn)
	r.limit.Store(1 << 30)
	r.c.start(r.tn)
	waitFor(t, "the raised limit recorded", func() bool { return recordedFor(r.c, r.tn.id) == 1<<30 })
	waitFor(t, "the learner caught up", func() bool {
		return r.tn.node.Status().Applied >= r.c.leader(5*time.Second).node.Status().Applied
	})
	if o := r.offered.Load(); o <= 2048 {
		t.Fatalf("the learner caught up with no snapshot over 2048 bytes offered (largest %d)", o)
	}
	if b := newestBytes(r.tn.node); b <= 2048 {
		t.Fatalf("the learner's newest snapshot is %d bytes: it installed none over its former limit", b)
	}
	for {
		err := r.c.leader(5*time.Second).node.Promote(ctx, "op:test", r.tn.id)
		if err == nil {
			break
		}
		if ctx.Err() != nil || !errs.HasCode(err, coreconsensus.CodeMembershipChangePending) && !errors.Is(err, coreconsensus.MembershipInvalid) {
			t.Fatalf("promoting the learner whose limit was raised: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if m, _ := r.c.leader(5 * time.Second).node.Membership().Member(r.tn.id); !m.Voter {
		t.Fatal("the learner whose limit was raised is not a voter")
	}
}

// stateLimit has n state limit as target's MaxSnapshotBytes.
func stateLimit(ctx context.Context, n *Node, target coreconsensus.NodeID, limit int64) error {
	return n.admin(ctx, coreconsensus.EntryMembership, wire.AdminValue{
		Operator: "op:test", Change: wire.ChangeSnapshotLimit, Target: target,
		Membership: coreconsensus.MembershipValue{Members: []coreconsensus.MemberValue{{ID: target, MaxSnapshotBytes: limit}}},
	})
}

// Only a member states its own limit: a change of another member's,
// submitted on a follower or on the leader, is refused MEMBERSHIP_INVALID,
// and the membership keeps what it records; a member stating its own is
// recorded (C1, #677).
func TestASnapshotLimitChangeFromAnotherNodeIsRefused(t *testing.T) {
	c := newClusterWith(t, &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{6, 7, 7, 3}, size: 3})
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, tc := range []struct {
		name   string
		on, of *testNode
	}{
		{"a follower stating the leader's limit", f, l},
		{"the leader stating a follower's limit", l, f},
	} {
		if err := stateLimit(ctx, tc.on.node, tc.of.id, 1<<20); !errors.Is(err, coreconsensus.MembershipInvalid) {
			t.Errorf("%s: %v, want MEMBERSHIP_INVALID", tc.name, err)
		}
	}
	if a, b := recordedFor(c, l.id), recordedFor(c, f.id); a != 0 || b != 0 {
		t.Fatalf("a refused change was recorded: the leader's limit %d, the follower's %d", a, b)
	}
	// The limit the follower runs with, so that it proposes nothing itself.
	if err := stateLimit(ctx, f.node, f.id, 1<<30); err != nil {
		t.Fatalf("the follower stating its own limit: %v", err)
	}
	if got := recordedFor(c, f.id); got != 1<<30 {
		t.Fatalf("the follower's own limit is recorded as %d, want %d", got, 1<<30)
	}
}

// A cluster under a rolling upgrade, its leader of the release before #677,
// which knows no ChangeSnapshotLimit and refuses it MEMBERSHIP_INVALID. A
// learner raised from 4096 bytes to 1 GiB and restarted is refused, waits,
// and states its limit again; once a leader of this release leads it is
// recorded, and the older node, a follower now, adopts the configuration
// that carries it: no protocol raise is needed (A1, #677).
func TestAnOlderLeaderRefusesTheLimitAndANewerOneRecordsIt(t *testing.T) {
	var old, joiner atomic.Uint64
	var limit atomic.Int64
	limit.Store(4096)
	logs := &attrLogger{}
	var mu sync.Mutex
	taps := map[coreconsensus.NodeID]*tapTransport{}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{6, 7, 7, 4}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		tp := taps[tn.id]
		if tp == nil {
			tp = &tapTransport{MemoryTransport: NewMemoryTransport(c.net, tn.id), id: tn.id, old: &old}
			taps[tn.id] = tp
		}
		cfg.Transport = tp
		if uint64(tn.id) == joiner.Load() {
			cfg.MaxSnapshotBytes, cfg.SnapshotEveryBytes, cfg.Logger = limit.Load(), 1024, logs
		}
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	old.Store(uint64(l.id))
	tn := newJoiner(t, c, "slot-joiner")
	joiner.Store(uint64(tn.id))
	joinAnnouncing(t, c, tn, 4096)
	c.kill(tn)
	limit.Store(1 << 30)
	c.start(tn)
	waitFor(t, "the older leader's refusal", func() bool {
		return warned(logs, msgAdminOperation, "change", fmt.Sprint(wire.ChangeSnapshotLimit), "outcome", "MEMBERSHIP_INVALID")
	})
	if got := recordedFor(c, tn.id); got != 4096 {
		t.Fatalf("the older leader recorded %d for the learner", got)
	}
	var n *testNode
	for _, x := range c.nodes[:3] {
		if x != l {
			n = x
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for c.leader(5*time.Second) != n {
		if err := c.leader(5*time.Second).node.TransferLeadership(ctx, "op:test", n.id); err != nil && ctx.Err() != nil {
			t.Fatalf("transfer to %d: %v", n.id, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitFor(t, "the limit recorded under the newer leader", func() bool {
		m, _ := n.node.Membership().Member(tn.id)
		return m.MaxSnapshotBytes == 1<<30
	})
	waitFor(t, "the older follower adopting it", func() bool {
		m, _ := l.node.Membership().Member(tn.id)
		return m.MaxSnapshotBytes == 1<<30
	})
}
