package consensus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// quotaLog reports its log's bytes as 1 KiB per entry held, so the
// no-space guard runs on a memory log, and free, when set, as its
// filesystem's free bytes.
type quotaLog struct {
	*MemoryLog
	free *atomic.Int64
}

func (l quotaLog) logBytes() int64 {
	first, last := l.Bounds()
	if last < first {
		return 0
	}
	return int64(last-first+1) << 10
}

func (l quotaLog) freeBytes() int64 {
	if l.free == nil {
		return -1
	}
	return l.free.Load()
}

// learnerOutgrown runs a cluster of three whose learner announced a
// MaxSnapshotBytes of 2048, with a log quota of 300 KiB less a reserve of
// 100 KiB, and, once it caught up, its filesystem short when shortFS; it
// writes until the cluster refuses one NO_SPACE or 600 writes of about 100
// bytes outgrew the learner's limit, and returns the leader, the learner
// and that refusal.
func learnerOutgrown(t *testing.T, shortFS bool) (l, tn *testNode, refused error) {
	t.Helper()
	var learnerID coreconsensus.NodeID
	free := new(atomic.Int64)
	free.Store(-1)
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{4, 4, 4}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		if learnerID != 0 && tn.id == learnerID {
			cfg.Log = quotaLog{MemoryLog: tn.log, free: free}
			cfg.MaxSnapshotBytes, cfg.SnapshotEveryBytes = 2048, 1024
			cfg.MaxLogBytes, cfg.LogReserveBytes = 300<<10, 100<<10
		}
	}
	newClusterWith(t, c)
	l = c.settledLeader(5 * time.Second)
	for i := range 10 {
		propose(t, l.node, fmt.Sprintf("k%03d=v", i))
	}
	tn = &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
	id, _, err := InitLog(tn.log, c.cid, "slot-learner")
	if err != nil {
		t.Fatal(err)
	}
	tn.id, learnerID = id, id
	c.nodes = append(c.nodes, tn)
	c.start(tn)
	st := must(tn.log.Stamp())(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, err := NewMemoryTransport(c.net, id).Join(ctx, coreconsensus.JoinConfig{Stamp: st, Address: "mem", MaxSnapshotBytes: 2048})
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("join: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	waitFor(t, "learner caught up", func() bool { return tn.node.Status().Applied >= l.node.Status().Applied })
	if shortFS {
		free.Store(1)
		waitFor(t, "the learner's disk short", tn.node.space.short.Load)
	}
	big := strings.Repeat("x", 100)
	for i := 0; i < 600 && refused == nil; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		lead := c.leader(5 * time.Second)
		_, _, err := lead.node.Propose(ctx, fmt.Appendf(nil, "big%04d=%s", i, big))
		cancel()
		if errors.Is(err, coreconsensus.NoSpace) {
			refused = err
		}
	}
	lf, ll := l.log.Bounds()
	nf, nl := tn.log.Bounds()
	t.Logf("leader log [%d,%d], learner log [%d,%d], cluster NoSpace alarm %v, learner's own limit 2048, leader's newest snapshot %d bytes",
		lf, ll, nf, nl, l.node.Status().NoSpace, newestBytes(l.node))
	return l, tn, refused
}

// newestBytes is the size of n's newest snapshot.
func newestBytes(n *Node) int64 {
	n.snapState.mu.Lock()
	defer n.snapState.mu.Unlock()
	return n.snapState.newestBytes
}

// A learner announcing a MaxSnapshotBytes the state outgrew cuts nothing
// more, since its own cuts are held to its own limit. Its disk short, its
// no-space alarm refused every write of the cluster (#600). A learner
// raises no alarm: it refuses its own proposals only.
func TestALearnerTheStateOutgrewDoesNotRaiseTheClusterNoSpace(t *testing.T) {
	_, tn, refused := learnerOutgrown(t, true)
	if refused != nil {
		t.Errorf("a learner whose disk is short and whose own MaxSnapshotBytes the state outgrew raises a NO_SPACE alarm that refuses every write of the cluster: %v", refused)
	}
	if !tn.node.space.short.Load() {
		t.Error("the learner's disk was never short: the scenario did not run")
	}
}

// The leader kept feeding the log to that learner, which could compact
// none of it: its log passed its quota and grew until its filesystem was
// full, and it stopped STORAGE_FAILED (#613). The leader sends it no more
// entries once its newest snapshot is over the learner's limit: the
// learner's log stays under its quota.
func TestALearnerTheStateOutgrewIsNotFedTheLogPastItsQuota(t *testing.T) {
	l, tn, refused := learnerOutgrown(t, false)
	if refused != nil {
		t.Errorf("the cluster refused a write NO_SPACE: %v", refused)
	}
	if newest := newestBytes(l.node); newest <= 2048 {
		t.Fatalf("setup: the leader's newest snapshot is %d bytes, within the learner's limit", newest)
	}
	if tn.node.space.short.Load() {
		t.Error("the learner, which compacts nothing, was fed the log until it passed its quota")
	}
	if b := (quotaLog{MemoryLog: tn.log}).logBytes(); b+(100<<10) >= 300<<10 {
		t.Errorf("the learner's log holds %d KiB, past its quota less its reserve", b>>10)
	}
}
