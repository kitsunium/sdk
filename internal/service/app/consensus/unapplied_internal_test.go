package consensus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// slowApplyKV is the test's kv, whose Apply waits delay first: a follower
// whose state machine is slower than the leader's.
type slowApplyKV struct {
	inner *kv
	delay *atomic.Int64
}

func (s *slowApplyKV) Apply(e coreconsensus.EntryValue) any {
	if d := s.delay.Load(); d > 0 {
		time.Sleep(time.Duration(d))
	}
	return s.inner.Apply(e)
}

func (s *slowApplyKV) Snapshot() (coreconsensus.Snapshot, error) { return s.inner.Snapshot() }

func (s *slowApplyKV) Restore(r io.Reader, m coreconsensus.SnapshotMetaValue) error {
	return s.inner.Restore(r, m)
}

// TestAFollowerSlowToApplyHoldsNoMoreThanMaxApplyLagBytes: ADR 0152 D6
// bounds the gap between commit and applied by MaxApplyLagBytes — "the
// leader stops admitting proposals with OVERLOADED until apply catches up,
// and a follower stops reading new entries from its log into memory". Only
// the first half existed: a follower's raft handed every committed entry to
// its apply goroutine, whatever its lag, and a follower slower to apply than
// the leader held in memory everything it was behind by (#664). It now
// holds MaxApplyLagBytes at most, and catches up once its machine does, the
// same state as the leader's.
func TestAFollowerSlowToApplyHoldsNoMoreThanMaxApplyLagBytes(t *testing.T) {
	const lagBound = 1 << 20
	var mu sync.Mutex
	delays := map[coreconsensus.NodeID]*atomic.Int64{}
	delayOf := func(id coreconsensus.NodeID) *atomic.Int64 {
		mu.Lock()
		defer mu.Unlock()
		if delays[id] == nil {
			delays[id] = new(atomic.Int64)
		}
		return delays[id]
	}
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{0x5d, 0x1a}, every: 1 << 30, size: 3,
		timing: steadyTiming(),
		configure: func(tn *testNode, cfg *Config) {
			cfg.Machine = &slowApplyKV{inner: tn.sm, delay: delayOf(tn.id)}
			cfg.MaxApplyLagBytes = lagBound
		},
	})
	lead := c.settledLeader(10 * time.Second)
	var slow *testNode
	for _, tn := range c.nodes {
		if tn != lead {
			slow = tn
			break
		}
	}
	delayOf(slow.id).Store(int64(2 * time.Millisecond))
	defer delayOf(slow.id).Store(0)

	payload := make([]byte, 4096)
	copy(payload, "k=")
	for i := 2; i < len(payload); i++ {
		payload[i] = 'v'
	}
	const writers, each = 16, 200
	var refused, failed atomic.Int64
	var maxLag atomic.Int64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			if l := slow.node.applyLag.Load(); l > maxLag.Load() {
				maxLag.Store(l)
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for k := range each {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				data := append(fmt.Appendf(nil, "w%d=%d:", w, k), payload[2:]...)
				_, _, err := lead.node.Propose(ctx, data)
				cancel()
				switch {
				case err == nil:
				case errs.HasCode(err, coreconsensus.CodeOverloaded):
					refused.Add(1)
				case errors.Is(err, coreconsensus.NotLeader):
					failed.Add(1)
				default:
					failed.Add(1)
				}
			}
		})
	}
	wg.Wait()
	st, fst := lead.node.Status(), slow.node.Status()
	t.Logf("leader %d committed %d, applied %d; slow follower %d applied %d; leader refused %d OVERLOADED, %d other failures; the follower's apply lag peaked at %d bytes, its bound MaxApplyLagBytes is %d",
		lead.id, st.Commit, st.Applied, slow.id, fst.Applied, refused.Load(), failed.Load(), maxLag.Load(), int64(lagBound))
	if fst.Applied >= st.Commit {
		t.Fatalf("setup: the slow follower kept up (applied %d of %d): nothing was held back", fst.Applied, st.Commit)
	}
	// Apply holds the entries handed in memory, MaxApplyLagBytes at most,
	// or the batch it reads back, MaxApplyLagBytes at most too, never both:
	// nothing is handed in memory behind a read, and a read comes after the
	// entries handed before it were applied.
	if maxLag.Load() > lagBound {
		t.Errorf("a follower slower to apply held %d bytes of committed entries unapplied in memory, past MaxApplyLagBytes (%d): D6's bound holds on the leader only",
			maxLag.Load(), int64(lagBound))
	}

	// The follower catches up, still within its bound, once its machine
	// is fast again, and ends with the leader's state.
	delayOf(slow.id).Store(0)
	commit := st.Commit
	deadline := time.Now().Add(30 * time.Second)
	for slow.node.Status().Applied < commit && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	<-sampled
	if got := slow.node.Status().Applied; got < commit {
		t.Fatalf("the slow follower applied %d of %d within 30 s once its machine was fast again", got, commit)
	}
	if maxLag.Load() > lagBound {
		t.Errorf("catching up, the follower's apply lag peaked at %d bytes, past MaxApplyLagBytes (%d)", maxLag.Load(), int64(lagBound))
	}
	waitFor(t, "the follower's apply lag to drain to 0", func() bool { return slow.node.applyLag.Load() == 0 })
	// A proposal whose caller gave up may have committed after the commit
	// read above: both nodes are compared at the leader's commit now,
	// which nothing moves any more.
	final := lead.node.Status().Commit
	waitFor(t, "both nodes to apply the leader's commit", func() bool {
		return lead.node.Status().Applied >= final && slow.node.Status().Applied >= final
	})
	want, got := kvCopy(lead.sm), kvCopy(slow.sm)
	if len(want) != len(got) {
		t.Fatalf("the follower holds %d keys, the leader %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("key %q: the follower holds %.12q, the leader %.12q", k, got[k], v)
		}
	}
}

// kvCopy is a copy of what s holds.
func kvCopy(s *kv) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.m)
}

// TestALeaderSlowToApplyStillRefusesForwardsPastMaxApplyLagBytes: raft now
// hands apply no more than MaxApplyLagBytes, so the bytes the apply
// goroutine holds no longer measure the gap between commit and applied. A
// leader whose apply is slow must still refuse proposals OVERLOADED while
// raft holds committed entries back (D6). Its local proposals are also
// bounded by MaxPendingBytes, released at apply; a forwarded command is
// bounded only while uncommitted, so without the refusal a follower's
// forwards grew the leader's gap with no bound (#664).
func TestALeaderSlowToApplyStillRefusesForwardsPastMaxApplyLagBytes(t *testing.T) {
	const lagBound, forwardBound, payloadSize = 64 << 10, 256 << 10, 4096
	var mu sync.Mutex
	delays := map[coreconsensus.NodeID]*atomic.Int64{}
	delayOf := func(id coreconsensus.NodeID) *atomic.Int64 {
		mu.Lock()
		defer mu.Unlock()
		if delays[id] == nil {
			delays[id] = new(atomic.Int64)
		}
		return delays[id]
	}
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{0x5d, 0x1b}, every: 1 << 30, size: 3,
		timing: steadyTiming(),
		configure: func(tn *testNode, cfg *Config) {
			cfg.Machine = &slowApplyKV{inner: tn.sm, delay: delayOf(tn.id)}
			cfg.MaxApplyLagBytes, cfg.MaxPendingBytes, cfg.MaxEntryBytes = lagBound, forwardBound, 8<<10
		},
	})
	lead := c.settledLeader(10 * time.Second)
	f := c.otherThan(lead)
	delayOf(lead.id).Store(int64(3 * time.Millisecond))
	defer delayOf(lead.id).Store(0)

	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = 'v'
	}
	var refused, failed, ok atomic.Int64
	var maxGap atomic.Int64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			st := lead.node.Status()
			if gap := int64(st.Commit-min(st.Applied, st.Commit)) * payloadSize; gap > maxGap.Load() {
				maxGap.Store(gap)
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			for k := range 150 {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				data := append(fmt.Appendf(nil, "f%d=%d:", w, k), payload[16:]...)
				_, _, err := f.node.Propose(ctx, data)
				cancel()
				switch {
				case err == nil:
					ok.Add(1)
				case errs.HasCode(err, coreconsensus.CodeOverloaded):
					refused.Add(1)
				default:
					failed.Add(1)
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	<-sampled
	t.Logf("forwarded through %d to leader %d: %d committed, %d refused OVERLOADED, %d other failures; the leader's gap between commit and applied peaked at %.0f KiB, its MaxApplyLagBytes %d KiB and the forwards' bound %d KiB",
		f.id, lead.id, ok.Load(), refused.Load(), failed.Load(), float64(maxGap.Load())/1024, lagBound>>10, forwardBound>>10)
	if refused.Load() == 0 {
		t.Errorf("a leader slow to apply refused no forward OVERLOADED")
	}
	// Past the bound, what one batch of forwards adds at most before the
	// refusal holds, and a batch of slack for the sampling.
	if limit := int64(lagBound + forwardBound + (64 << 10)); maxGap.Load() > limit {
		t.Errorf("the leader's gap between commit and applied peaked at %.0f KiB, past MaxApplyLagBytes plus the forwards' bound (%d KiB): forwards were admitted while raft held committed entries back",
			float64(maxGap.Load())/1024, limit>>10)
	}
	delayOf(lead.id).Store(0)
	tok := proposeThroughLag(t, c, "last=1")
	c.waitApplied(tok.Index, 30*time.Second)
	want := kvCopy(lead.sm)
	for _, tn := range c.nodes {
		if got := kvCopy(tn.sm); len(got) != len(want) {
			t.Fatalf("node %d holds %d keys, the leader %d", tn.id, len(got), len(want))
		}
	}
}

// dropKV is a BatchingStateMachine that lets go of the payloads it was
// handed once it applied them, as a machine that frees memory may.
type dropKV struct{ *kv }

func (b dropKV) ApplyBatch(es []coreconsensus.EntryValue, results []any) {
	for i := range es {
		results[i] = b.Apply(es[i])
		es[i].Data = nil
	}
}

// A batch leaves the apply lag once the machine applied it (#664), by the
// bytes it was handed: measured once the machine returned, the payloads a
// machine let go of stayed in the lag for good, raft handed that node
// nothing more past MaxApplyLagBytes, and the leader refused every
// proposal.
func TestAMachineThatDropsItsPayloadsLeavesNoApplyLag(t *testing.T) {
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{0x5d, 0x1e},
		configure: func(tn *testNode, cfg *Config) {
			cfg.Machine = dropKV{kv: tn.sm}
			cfg.MaxApplyLagBytes = 64 << 10
		},
	})
	l := c.leader(5 * time.Second)
	payload := make([]byte, 1024)
	for i := range payload {
		payload[i] = 'x'
	}
	var wg sync.WaitGroup
	for w := range 16 {
		wg.Go(func() {
			for i := range 16 {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				// A refusal or a leadership change during the load is not
				// the subject: the lag once everything is applied is.
				_, _, _ = l.node.Propose(ctx, append(fmt.Appendf(nil, "k%d-%d=", w, i), payload...))
				cancel()
			}
		})
	}
	wg.Wait()
	tok := proposeThroughLag(t, c, "last=1")
	c.waitApplied(tok.Index, 10*time.Second)
	for _, tn := range c.nodes {
		waitFor(t, fmt.Sprintf("node %d's apply lag to drain to 0", tn.id), func() bool { return tn.node.applyLag.Load() == 0 })
	}
	// Bytes several times MaxApplyLagBytes went through: a leak would now
	// refuse every proposal, and leave the followers behind.
	for i := range 20 {
		tok = proposeThroughLag(t, c, fmt.Sprintf("after%d=%s", i, payload))
	}
	c.waitApplied(tok.Index, 10*time.Second)
}
