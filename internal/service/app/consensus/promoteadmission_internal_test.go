package consensus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// #722: Promote made a voter of a joiner that did not hold the entry that
// admits it: the leader checked only match+PromoteLag >= commit, and while
// the commit is at most PromoteLag a learner heard once at match 0 passed.
// The joiner's stamp stays joining until its log holds that entry committed
// and durable (#695), so it refused every vote and PreVote. Once the leader
// failed, three live voters of four elected no one, and the joiner, which
// learns its admission from a leader only, never voted.

// heldFirstSyncLog is a joiner's in-memory log whose disk is slow: the
// first Sync that covers appended entries waits until release is closed.
// Hard-state-only syncs pass, so the joiner answers its leader (term,
// heartbeats) as on a real disk that is merely slow on the first batch.
type heldFirstSyncLog struct {
	*MemoryLog
	mu      sync.Mutex
	pending bool
	hold    bool
	release chan struct{}
	held    atomic.Bool
}

func (l *heldFirstSyncLog) Append(entries []coreconsensus.EntryValue, hs *coreconsensus.HardState) error {
	if len(entries) > 0 {
		l.mu.Lock()
		l.pending = true
		l.mu.Unlock()
	}
	return l.MemoryLog.Append(entries, hs)
}

func (l *heldFirstSyncLog) Sync() error {
	l.mu.Lock()
	wait := l.pending && l.hold
	l.pending = false
	if wait {
		l.hold = false
	}
	l.mu.Unlock()
	if wait {
		l.held.Store(true)
		<-l.release
	}
	return l.MemoryLog.Sync()
}

// joinUntilAdmitted sends tn's JOIN until a member answers it, and returns
// the position of the entry that admits it.
func joinUntilAdmitted(t *testing.T, ctx context.Context, c *cluster, tn *testNode) coreconsensus.PositionValue {
	t.Helper()
	st := must(tn.log.Stamp())(t)
	for {
		admitted, err := NewMemoryTransport(c.net, tn.id).Join(ctx, coreconsensus.JoinConfig{Stamp: st, Address: "mem"})
		if err == nil {
			return admitted
		}
		if ctx.Err() != nil {
			t.Fatalf("join: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

// joinerBehindABacklog is a cluster of three whose log holds a backlog a
// joiner's first batch (MaxBatchBytes) does not cover, and a joiner J
// started on log, which wrap may replace; J is not joined yet.
func joinerBehindABacklog(t *testing.T, cid byte, wrap func(*MemoryLog) coreconsensus.LogStore) (*cluster, *testNode) {
	t.Helper()
	var jlog coreconsensus.LogStore
	var jmem *MemoryLog
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{0x72, 0x20, cid}, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		cfg.MaxBatchBytes, cfg.MaxEntryBytes = 4096, 4096
		if jlog != nil && tn.log == jmem {
			cfg.Log = jlog
		}
	}
	c = newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	for i := range 60 {
		propose(t, l.node, fmt.Sprintf("k%02d=%s", i, strings.Repeat("x", 500)))
	}
	tn := &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
	jid, _, err := InitLog(tn.log, c.cid, "slot-j")
	if err != nil {
		t.Fatal(err)
	}
	tn.id, jmem = jid, tn.log
	jlog = wrap(tn.log)
	c.nodes = append(c.nodes, tn)
	c.start(tn)
	return c, tn
}

// TestAPromotionWaitsForTheJoinerToHoldItsAdmission: J joins and answers
// its leader, but its first sync carrying entries is held, a slow disk.
// Promote is refused while J holds nothing, then admitted once J holds
// its admission. The leader then fails: J, a voter whose stamp is a
// member's, votes, and the three voters left of four elect a leader.
func TestAPromotionWaitsForTheJoinerToHoldItsAdmission(t *testing.T) {
	var jlog *heldFirstSyncLog
	c, tn := joinerBehindABacklog(t, 1, func(m *MemoryLog) coreconsensus.LogStore {
		jlog = &heldFirstSyncLog{MemoryLog: m, hold: true, release: make(chan struct{})}
		return jlog
	})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(jlog.release) }) }
	t.Cleanup(release) // runs before the cluster's HardClose (LIFO)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admitted := joinUntilAdmitted(t, ctx, c, tn)
	for deadline := time.Now().Add(5 * time.Second); !jlog.held.Load(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("setup: the joiner never received a batch")
		}
	}

	// While J's first sync is held, J acknowledges nothing: every
	// promotion is refused, MEMBERSHIP_INVALID once its admission commits.
	l := c.leader(5 * time.Second)
	var refusal error
	promotedEarly := false
	for deadline := time.Now().Add(500 * time.Millisecond); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		err := l.node.Promote(ctx, "op:test", tn.id)
		if err == nil {
			_, jlast := tn.log.Bounds()
			t.Errorf("J was promoted while its first sync was held: admitted at %d, its log ends at %d, it acknowledged nothing", admitted.Index, jlast)
			promotedEarly = true
			break
		}
		refusal = err
	}
	if !promotedEarly {
		if !errors.Is(refusal, coreconsensus.MembershipInvalid) {
			t.Fatalf("while J held nothing, Promote was last refused %v, want MEMBERSHIP_INVALID", refusal)
		}
		// J's disk catches up; its acknowledgements reach its admission.
		release()
		retryAdmin(t, "promote J", func() error { return l.node.Promote(ctx, "op:test", tn.id) })
		if _, jlast := tn.log.Bounds(); jlast < admitted.Index {
			t.Fatalf("J was promoted at a log ending at %d, below its admission at %d", jlast, admitted.Index)
		}
	}
	others := make([]*testNode, 0, 2)
	for _, x := range c.nodes {
		if x != l && x != tn {
			others = append(others, x)
		}
	}
	// One voter of four fails: the leader.
	c.kill(l)
	c.net.Isolate(l.id, true)
	release()
	wait := 25 * testTimingFast().ElectionTimeoutMax
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		for _, x := range append(others, tn) {
			if x.node.Status().Role == coreconsensus.RoleLeader {
				return
			}
		}
	}
	js := must(tn.log.Stamp())(t)
	_, jlast := tn.log.Bounds()
	t.Errorf("three of four voters alive for %v and no leader: J (stamp state %d, log to %d, commit %d) never learnt its admission at %d and refuses every vote",
		wait, js.State, jlast, tn.node.Status().Commit, admitted.Index)
}

// TestAPromotionRetriedFromTheJoinWaitsForTheAdmission: no slow disk.
// Promote is retried from the JOIN's answer on, as a product's automation
// does: it is admitted only once J's log holds its admission, and J's
// stamp turns a member's without anything else.
func TestAPromotionRetriedFromTheJoinWaitsForTheAdmission(t *testing.T) {
	c, tn := joinerBehindABacklog(t, 2, func(m *MemoryLog) coreconsensus.LogStore { return m })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admitted := joinUntilAdmitted(t, ctx, c, tn)
	l := c.leader(5 * time.Second)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		err := l.node.Promote(ctx, "op:test", tn.id)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("promote: %v", err)
		}
	}
	accepted := time.Now()
	if _, jlast := tn.log.Bounds(); jlast < admitted.Index {
		t.Fatalf("J was promoted while its log ended at %d, below its admission at %d: a voter that votes for no one", jlast, admitted.Index)
	}
	for deadline := accepted.Add(5 * time.Second); must(tn.log.Stamp())(t).State != coreconsensus.StampMember; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("J, promoted with its admission at %d in its log, never turned a member: %+v", admitted.Index, tn.node.Status())
		}
	}
	t.Logf("J's stamp turned a member's %v after its promotion was admitted", time.Since(accepted))
}
