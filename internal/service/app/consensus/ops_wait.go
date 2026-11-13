package consensus

import (
	"context"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The read side of the node's operations: Barrier confirms the leadership
// and waits for the commit it read (D9), WaitApplied waits for one token
// and checks the entry applied at its index is its own (#509).

// Barrier returns once the local state machine reflects every write
// committed before the call (D9).
// Reads queued in one turn share one round.
func (n *Node) Barrier(ctx context.Context) (coreconsensus.TokenValue, error) {
	if n.stopped.Load() {
		return coreconsensus.TokenValue{}, n.stoppedErr()
	}
	w := &readWait{done: make(chan readResult, 1)}
	noLease := n.cfg.Reads != ReadLease
	start := n.clk.Now()
	mode := "read_index"
	if !noLease {
		mode = "lease"
	}
	defer func() { n.obs.record(mBarrierLatency, n.clk.Since(start).Seconds(), coreotel.String("mode", mode)) }()
	// The loop registers every read queued before its turn and opens one
	// round for them all (onReads, #1108).
	n.readq.push(w)
	res, ok := n.awaitRead(ctx, w)
	if !ok {
		if ctx.Err() != nil {
			return coreconsensus.TokenValue{}, origin(coreconsensus.LeadershipUnconfirmed, errs.String("cause", ctx.Err().Error()))
		}
		return coreconsensus.TokenValue{}, n.stoppedErr()
	}
	switch res.code {
	case 0:
	case coreconsensus.CodeNodeStopped:
		return coreconsensus.TokenValue{}, n.stoppedErr()
	default:
		return coreconsensus.TokenValue{}, origin(sentinelOf(res.code), errs.String("leader", n.Status().Leader.String()))
	}
	if !n.waitApplied(ctx.Done(), res.index) {
		if ctx.Err() != nil {
			return coreconsensus.TokenValue{}, ctx.Err()
		}
		return coreconsensus.TokenValue{}, n.stoppedErr()
	}
	n.am.Lock()
	tok := n.token(coreconsensus.PositionValue{Term: n.appliedTerm, Index: n.applied})
	n.am.Unlock()
	return tok, nil
}

// awaitRead waits for the loop's answer to w. It reports false when ctx
// ended first, or when the loop ended without answering: a read queued after
// the loop's last turn is answered by no one.
func (n *Node) awaitRead(ctx context.Context, w *readWait) (readResult, bool) {
	select {
	case res := <-w.done:
		return res, true
	case <-ctx.Done():
		w.gone.Store(true)
		return readResult{}, false
	case <-n.loopDone:
		select {
		case res := <-w.done:
			return res, true
		default:
			return readResult{}, false
		}
	}
}

// WaitApplied returns once the local node has applied t, the entry of t's
// term at t's index. A token of another lineage is LINEAGE_MISMATCH. A token
// whose index holds an entry of another term is ENTRY_SUPERSEDED, carrying
// the token of the entry applied there, or LINEAGE_MISMATCH when it is one of
// the predecessor's within the index Recover kept, as is a token of the
// predecessor's whose term this node can no longer read there.
func (n *Node) WaitApplied(ctx context.Context, t coreconsensus.TokenValue) error {
	want := n.token(t.Position())
	if t.Cluster != want.Cluster || t.Epoch != want.Epoch {
		return origin(coreconsensus.LineageMismatch)
	}
	if !n.waitApplied(ctx.Done(), t.Index) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return n.stoppedErr()
	}
	return n.appliedAs(t)
}

// appliedAs checks that the entry applied at t's index is t's: the index
// alone named the predecessor's identity, and since Recover adopts the
// verified suffix past the commit the survivor persisted (D17), an entry it
// kept there may be of another term than the write the old cluster
// committed and acknowledged at that index. That write is lost, and
// WaitApplied reported it applied (#509). Where the term cannot be read any
// more, a token of the current history is trusted by Log Matching, and so
// is one of the predecessor's at or below the index the predecessor had
// committed there (AdoptedFrom, #509 phase 2); one in the adopted range
// above it is not: that range may hold such a suffix.
func (n *Node) appliedAs(t coreconsensus.TokenValue) error {
	term, known, ok := n.appliedTermAt(t.Index)
	if !ok {
		return n.stoppedErr()
	}
	l := n.lineage()
	kept := l.Predecessor != (coreconsensus.ClusterID{}) && t.Index <= l.KeptIndex && t.Index > l.AdoptedFrom
	switch {
	case known && term == t.Term, !known && !kept:
		return nil
	case !known:
		return origin(coreconsensus.LineageMismatch, errs.String("why", "the predecessor's entry at this index can no longer be read"))
	case kept:
		return origin(coreconsensus.LineageMismatch, errs.String("why", "the predecessor's entry at this index is not the one kept"))
	default:
		return withToken(coreconsensus.EntrySuperseded, n.token(coreconsensus.PositionValue{Term: term, Index: t.Index}))
	}
}

// appliedTermAt is the term of the entry applied at index, which the caller
// waited for: from the step function, whose committed log is the truth, then
// from the store while index is one this node applied from its log, which
// the store holds as committed. known is false when neither holds it any
// more; ok is false when the node stopped.
func (n *Node) appliedTermAt(index uint64) (term uint64, known, ok bool) {
	if !n.exec(func() {
		if index <= n.r.Status().Commit {
			term, known = n.r.TermAt(index)
		}
	}) {
		return 0, false, false
	}
	if known || index > n.logApplied.Load() {
		return term, known, true
	}
	if first, _ := n.cfg.Log.Bounds(); index+1 < first {
		return 0, false, true
	}
	t, err := n.cfg.Log.Term(index)
	return t, err == nil, true
}
