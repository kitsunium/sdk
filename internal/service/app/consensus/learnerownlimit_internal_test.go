package consensus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// quotaLog reports its log's bytes as 1 KiB per entry held, so the
// no-space guard runs on a memory log.
type quotaLog struct{ *MemoryLog }

func (l quotaLog) logBytes() int64 {
	first, last := l.Bounds()
	if last < first {
		return 0
	}
	return int64(last-first+1) << 10
}
func (quotaLog) freeBytes() int64 { return -1 }

// A learner announcing a MaxSnapshotBytes the state outgrew cuts nothing
// more, since its own cuts are held to its own limit, while the leader keeps
// feeding it the log. Its log reached its quota and its no-space alarm
// refused every write of the cluster (#600). A learner raises no alarm: it
// refuses its own proposals only.
func TestALearnerTheStateOutgrewDoesNotRaiseTheClusterNoSpace(t *testing.T) {
	var learnerID coreconsensus.NodeID
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{4, 4, 4}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		if learnerID != 0 && tn.id == learnerID {
			cfg.Log = quotaLog{tn.log}
			cfg.MaxSnapshotBytes, cfg.SnapshotEveryBytes = 2048, 1024
			cfg.MaxLogBytes, cfg.LogReserveBytes = 300<<10, 100<<10
		}
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	for i := range 10 {
		propose(t, l.node, fmt.Sprintf("k%03d=v", i))
	}
	tn := &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
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
	var refused error
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
	t.Logf("leader log [%d,%d], learner log [%d,%d], cluster NoSpace alarm %v, learner's own limit 2048 < state",
		lf, ll, nf, nl, l.node.Status().NoSpace)
	if refused != nil {
		t.Errorf("a learner whose own MaxSnapshotBytes the state outgrew stopped compacting its own log and its NO_SPACE alarm refuses every write of the cluster: %v", refused)
	}
	if !tn.node.space.short.Load() {
		t.Error("the learner's log never reached its quota: the scenario did not run")
	}
}
