package consensus

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

// syncs is the number of Sync calls l completed.
func (l *hangLog) syncs() int {
	l.MemoryLog.mu.Lock()
	defer l.MemoryLog.mu.Unlock()
	return l.MemoryLog.syncCount
}

// stableOf is the last index tn holds durably, as its step function knows it.
func stableOf(tn *testNode) uint64 {
	var stable uint64
	tn.node.exec(func() { stable = tn.node.r.Status().Stable })
	return stable
}

// hangLogs gives every node of c a hangLog over its memory log, kept
// across restarts, and returns the lookup.
func hangLogs(c *cluster) func(*testNode) *hangLog {
	var mu sync.Mutex
	logs := map[*testNode]*hangLog{}
	get := func(tn *testNode) *hangLog {
		mu.Lock()
		defer mu.Unlock()
		hl := logs[tn]
		if hl == nil {
			hl = &hangLog{MemoryLog: tn.log}
			logs[tn] = hl
		}
		return hl
	}
	c.configure = func(tn *testNode, cfg *Config) { cfg.Log = get(tn) }
	return get
}

// #558: the storage actor paid one Sync per queued Ready. A follower whose
// fdatasync was held built a backlog of one request per append and one per
// commit advance, then drained it one Sync at a time (84 Syncs for 41
// entries), while the next commit, or the election if the leader was lost,
// waited for it. Every request taken together is now synced once, and a
// commit-only Ready is not synced on its own.
func TestABacklogOfWritesIsSyncedAsOneGroup(t *testing.T) {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 5, 8}, size: 3}
	logOf := hangLogs(c)
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	slow := c.otherThan(l)
	release := logOf(slow).hold()
	defer release()
	var last uint64
	for i := range 40 {
		last = propose(t, l.node, fmt.Sprintf("k%d=v", i)).Index
	}
	deadline := time.Now().Add(5 * time.Second)
	for slow.node.Status().Commit < last && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	held := stableOf(slow)
	if held >= last {
		t.Fatalf("setup: the held follower is durable through %d already", held)
	}
	before := logOf(slow).syncs()
	release()
	for stableOf(slow) < last {
		if time.Now().After(deadline) {
			t.Fatalf("the follower is durable through %d < %d after its Sync was released", stableOf(slow), last)
		}
		time.Sleep(time.Millisecond)
	}
	// The Sync that was held, then one for the whole backlog; one more
	// may start while the first one runs. A group of acknowledgements
	// alone, the replies to the heartbeats and resent appends that reach
	// the follower as it drains, is not synced: each one used to add a
	// Sync, and a slower runner counted 4 (#654).
	if n := logOf(slow).syncs() - before; n > 3 {
		t.Fatalf("released: %d Syncs to make entries %d..%d durable, want at most 3", n, held+1, last)
	}
}

// #654: a group that writes nothing once every earlier write is synced
// is not synced again. Its acknowledgements vouch for writes a sync made
// durable already; a follower draining its backlog took one such group
// per heartbeat or resent append it answered, a Sync each, and the count
// after a held Sync depended on how many arrived while it drained.
func TestAGroupOfAcknowledgementsAloneIsNotSynced(t *testing.T) {
	ml := &hangLog{MemoryLog: NewMemoryLog()}
	n := &Node{cfg: Config{Log: ml}, clk: clock.System, storec: make(chan storeDone, 8), stopc: make(chan struct{})}
	n.obs = newObs(&n.cfg)
	ack := func(seq uint64) []storeReq {
		return []storeReq{{rd: raft.ReadyValue{Seq: seq, AfterSync: []coreconsensus.MessageValue{{Kind: coreconsensus.MsgAppResp, To: 2}}}}}
	}
	var st storeState
	steps := []struct {
		what  string
		reqs  []storeReq
		syncs int
	}{
		{"an append", []storeReq{{rd: raft.ReadyValue{Seq: 1, Entries: ents(1, 3, 1)}}}, 1},
		{"an acknowledgement after it", ack(2), 1},
		{"a commit alone", []storeReq{{rd: raft.ReadyValue{Seq: 3, Commit: 3}}}, 1},
		{"an acknowledgement after the commit record", ack(4), 2},
		{"an acknowledgement after that sync", ack(5), 2},
	}
	for _, s := range steps {
		if !n.writeAll(s.reqs, &st) {
			t.Fatalf("%s: the actor ended", s.what)
		}
		if got := ml.syncs(); got != s.syncs {
			t.Fatalf("%s: %d Syncs, want %d", s.what, got, s.syncs)
		}
		if d := <-n.storec; d.seq != s.reqs[0].rd.Seq || len(d.after) != len(s.reqs[0].rd.AfterSync) {
			t.Fatalf("%s: completion %+v", s.what, d)
		}
	}
	if st.synced != 3 {
		t.Fatalf("durable through %d, want 3", st.synced)
	}
}

// slowLog is a memory log whose Sync takes delay.
type slowLog struct {
	*MemoryLog
	delay time.Duration
}

func (l *slowLog) Sync() error {
	time.Sleep(l.delay)
	return l.MemoryLog.Sync()
}

// BenchmarkSlowFollowerSyncs measures, on a 3-node cluster whose one
// follower takes 5 ms per Sync, the entries that follower makes durable per
// Sync while 32 writers commit 640 entries (#558). Before the group commit
// it paid one Sync per queued Ready.
func BenchmarkSlowFollowerSyncs(b *testing.B) {
	var entries, syncs float64
	for range b.N {
		e, s := runSlowFollower(b, 32, 20, 5*time.Millisecond)
		entries += e
		syncs += s
	}
	b.ReportMetric(entries/syncs, "entries/sync")
}

// runSlowFollower starts a 3-node cluster, slows one follower's Sync to
// delay, commits writers*each entries, waits for the slow follower to make
// them durable, and returns the entries and the Syncs it took.
func runSlowFollower(b *testing.B, writers, each int, delay time.Duration) (float64, float64) {
	b.StopTimer()
	c := &cluster{net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 5, 11}}
	logs := map[*testNode]*slowLog{}
	c.configure = func(tn *testNode, cfg *Config) { cfg.Log = logs[tn] }
	for i := range 3 {
		tn := &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
		id, inc, err := InitLog(tn.log, c.cid, fmt.Sprintf("slot-%d", i))
		if err != nil {
			b.Fatal(err)
		}
		tn.id = id
		logs[tn] = &slowLog{MemoryLog: tn.log}
		c.nodes = append(c.nodes, tn)
		c.members = append(c.members, coreconsensus.MemberValue{
			ID: id, Incarnation: inc, Slot: fmt.Sprintf("slot-%d", i), SPKI: MemorySPKI(id), Voter: true,
		})
	}
	for _, tn := range c.nodes {
		if err := Bootstrap(context.Background(), new(c.config(tn)), c.members); err != nil {
			b.Fatal(err)
		}
		n, err := NewNode(c.config(tn))
		if err != nil {
			b.Fatal(err)
		}
		tn.node = n
	}
	defer func() {
		for _, tn := range c.nodes {
			_ = tn.node.HardClose()
		}
	}()
	var l *testNode
	for l == nil {
		for _, tn := range c.nodes {
			if tn.node.Status().Role == coreconsensus.RoleLeader {
				l = tn
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	var slow *testNode
	for _, tn := range c.nodes {
		if tn != l {
			slow = tn
			break
		}
	}
	sl := logs[slow]
	sl.delay = delay
	sl.MemoryLog.mu.Lock()
	before := sl.MemoryLog.syncCount
	sl.MemoryLog.mu.Unlock()
	b.StartTimer()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var last uint64
	for w := range writers {
		wg.Go(func() {
			for i := range each {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				tok, _, err := l.node.Propose(ctx, fmt.Appendf(nil, "w%d-%d=v", w, i))
				cancel()
				if err != nil {
					b.Error(err)
					return
				}
				mu.Lock()
				last = max(last, tok.Index)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	for stableOf(slow) < last {
		time.Sleep(time.Millisecond)
	}
	b.StopTimer()
	defer b.StartTimer()
	sl.MemoryLog.mu.Lock()
	after := sl.MemoryLog.syncCount
	sl.MemoryLog.mu.Unlock()
	return float64(writers * each), float64(after - before)
}
