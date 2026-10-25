package consensus

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// A member's record holds the MaxSnapshotBytes it runs with: a voter's
// bounds every cut of every node (cutLimit), a learner's what it is offered
// and fed (tooLargeFor, feedOrNot), and Health and Promote read both. Its
// JOIN wrote it once, and nothing updated it: a member that raised its
// limit and restarted, #512's remedy, was held to the one it joined with,
// every cut failed SNAPSHOT_TOO_LARGE once the state outgrew it, no log was
// compacted, and the no-space alarm refused every write of the cluster until
// that member was replaced (#677). The member now states its own limit, in
// a ChangeSnapshotLimit the leader admits from it alone, and the no-space
// alarm does not refuse it.

const (
	// snapLimitFirstWait is the wait before a limit is proposed again after
	// a first refusal; it doubles at each refusal. A leader of the release
	// before #677 refuses every proposal.
	snapLimitFirstWait time.Duration = time.Second
	// snapLimitMaxWait bounds the doubled wait.
	snapLimitMaxWait time.Duration = time.Minute
	// snapLimitOperator is the operator a limit the engine states is
	// recorded under (D8).
	snapLimitOperator coreconsensus.Operator = "engine:snapshot-limit"
)

// snapLimitWatch is the state of watchSnapshotLimit. proposing is set while
// a proposal of the limit is in flight; the rest is under mu, which the
// proposal's goroutine takes to record its outcome.
type snapLimitWatch struct {
	proposing atomic.Bool
	mu        sync.Mutex
	// backoff is the wait after the last refusal, doubled at each one and
	// cleared once a proposal is admitted; nothing is proposed before
	// retryAt.
	backoff time.Duration
	retryAt time.Time
	// warned is the limit last found below the newest snapshot, so that it
	// is said once.
	warned int64
}

// watchSnapshotLimit proposes, at each tick, this node's MaxSnapshotBytes
// when the configuration lists it with another limit above 0, at most once
// per backoff, and while the node knows a leader. A recorded 0 announces no
// limit and is never replaced: recorded, the node's own limit would start
// bounding every cut of every node, the trap of #591. A value the
// configuration already holds, committed or appended, is not proposed
// again. The proposal runs on a goroutine counted in n.wg, which ends once
// it is answered, or once the node stops.
func (n *Node) watchSnapshotLimit(now time.Time) {
	w := &n.snapLimit
	if w.proposing.Load() {
		return
	}
	st := n.r.Status()
	m, ok := st.Conf.Member(n.stamp.ID)
	own := n.cfg.MaxSnapshotBytes
	if !ok || m.MaxSnapshotBytes <= 0 || m.MaxSnapshotBytes == own || st.Lead == 0 {
		return
	}
	w.mu.Lock()
	wait := now.Before(w.retryAt)
	w.mu.Unlock()
	if wait {
		return
	}
	w.proposing.Store(true)
	n.wg.Add(1)
	go n.proposeSnapshotLimit(m.MaxSnapshotBytes, own)
}

// proposeSnapshotLimit proposes own in place of recorded, the limit the
// configuration lists this node with, and records the outcome. A lowering
// below the newest snapshot is not proposed: recorded, a voter's would make
// every cut of every node fail SNAPSHOT_TOO_LARGE, the freeze of #677. It
// is said once, and checked again after the backoff, as Promote checks a
// learner's limit (#591).
func (n *Node) proposeSnapshotLimit(recorded, own int64) {
	defer n.wg.Done()
	defer n.snapLimit.proposing.Store(false)
	if own < recorded {
		if newest := n.newestSnapshotBytes(); newest > own {
			n.limitBelowNewest(recorded, newest)
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*n.Status().Timing.ElectionTimeoutMax+time.Second)
	defer cancel()
	self := []coreconsensus.MemberValue{{ID: n.stamp.ID, MaxSnapshotBytes: own}}
	// The outcome is logged by admin.
	err := n.admin(ctx, coreconsensus.EntryMembership, wire.AdminValue{
		Operator: string(snapLimitOperator), Change: wire.ChangeSnapshotLimit, Target: n.stamp.ID,
		Membership: coreconsensus.MembershipValue{Members: self},
	})
	n.snapLimit.answered(n.clk.Now(), err == nil)
}

// newestSnapshotBytes is the size of this node's newest snapshot: the one it
// cut or installed last, or the newest its store publishes, larger when a
// start under a lowered limit could not restore it (#512). It reads the
// store, and so never runs on the loop.
func (n *Node) newestSnapshotBytes() int64 {
	n.snapState.mu.Lock()
	newest := n.snapState.newestBytes
	n.snapState.mu.Unlock()
	if metas, err := n.cfg.Snapshots.List(); err == nil && len(metas) > 0 {
		newest = max(newest, snapshotSize(&metas[0]))
	}
	return newest
}

// limitBelowNewest says, once per limit, that this node's MaxSnapshotBytes
// is below newest and is not proposed in place of recorded, and waits as
// after a refusal.
func (n *Node) limitBelowNewest(recorded, newest int64) {
	w, own := &n.snapLimit, n.cfg.MaxSnapshotBytes
	w.mu.Lock()
	first := w.warned != own
	w.warned = own
	w.mu.Unlock()
	if first {
		n.obs.warn("consensus: this node's MaxSnapshotBytes is below its newest snapshot; the lowering is not proposed, and the membership keeps the limit it records",
			lattr("limit", uint64(own)), lattr("bytes", uint64(newest)), lattr("recorded", uint64(recorded)))
	}
	w.answered(n.clk.Now(), false)
}

// answered records the outcome of a proposal at now: admitted, the backoff
// is cleared; refused, the next one waits one second, doubling to a minute.
func (w *snapLimitWatch) answered(now time.Time, admitted bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if admitted {
		w.backoff, w.retryAt, w.warned = 0, time.Time{}, 0
		return
	}
	w.backoff = min(max(2*w.backoff, snapLimitFirstWait), snapLimitMaxWait)
	w.retryAt = now.Add(w.backoff)
}
