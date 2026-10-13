package consensus

import (
	"fmt"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

type applyItem struct {
	entries []coreconsensus.Entry
	restore *restoreReq
	save    *saveReq
	cut     bool
}

type restoreReq struct {
	meta coreconsensus.SnapshotMeta
	done chan error
}

// token builds an entry's lineage-scoped command identity: an entry kept by
// Recover keeps its PREDECESSOR identity (D10, R7-09).
func (n *Node) token(p coreconsensus.Position) coreconsensus.Token {
	l := n.ident
	if l.Predecessor != (coreconsensus.ClusterID{}) && p.Index <= l.KeptIndex {
		return coreconsensus.Token{Cluster: l.Predecessor, Epoch: l.PredecessorEpoch, Term: p.Term, Index: p.Index}
	}
	return coreconsensus.Token{Cluster: l.Cluster, Epoch: l.Epoch, Term: p.Term, Index: p.Index}
}

// applyLoop is the apply goroutine: every committed command in log order,
// on one goroutine (D3).
func (n *Node) applyLoop() {
	defer n.wg.Done()
	var items []applyItem
	for {
		select {
		case <-n.aq.signal:
		case <-n.stopc:
			return
		}
		items = n.aq.take(items)
		for _, it := range items {
			if n.stopped.Load() {
				return
			}
			if it.restore != nil {
				it.restore.done <- n.restoreFrom(it.restore.meta, 0)
				continue
			}
			if it.save != nil {
				n.save(it.save)
				continue
			}
			if it.cut {
				n.forceCut()
				continue
			}
			if !n.applyEntries(it.entries) {
				return
			}
			n.maybeSnapshot()
		}
	}
}

func (n *Node) applyEntries(es []coreconsensus.Entry) bool {
	batcher, batching := n.cfg.Machine.(coreconsensus.BatchingStateMachine)
	for i := 0; i < len(es); {
		e := &es[i]
		n.am.Lock()
		skip := e.Position.Index <= n.applied
		n.am.Unlock()
		if skip {
			i++
			continue
		}
		if e.Kind != coreconsensus.EntryCommand {
			n.applied1(e, nil)
			i++
			continue
		}
		j := i + 1
		if batching {
			for j < len(es) && es[j].Kind == coreconsensus.EntryCommand {
				j++
			}
		}
		results := make([]any, j-i)
		for k := i; k < j; k++ {
			es[k].Token = n.token(es[k].Position)
		}
		if err := n.safeApply(batcher, batching, es[i:j], results); err != nil {
			n.stop(err, false)
			return false
		}
		for k := i; k < j; k++ {
			n.applied1(&es[k], results[k-i])
		}
		i = j
	}
	return true
}

// forceCut cuts a snapshot at the applied index whatever SnapshotEvery says.
func (n *Node) forceCut() {
	n.am.Lock()
	idx, term := n.applied, n.appliedTerm
	n.am.Unlock()
	ss := &n.snapState
	ss.mu.Lock()
	if ss.inFlight || idx == 0 || idx <= ss.last {
		ss.mu.Unlock()
		return
	}
	ss.inFlight = true
	ss.mu.Unlock()
	if err := n.cut(idx, term, nil); err != nil {
		ss.mu.Lock()
		ss.inFlight = false
		ss.mu.Unlock()
	}
}

// safeApply calls the state machine; a panic stops the node with
// STATE_MACHINE_FAILED, and the entry IS committed (D3).
func (n *Node) safeApply(b coreconsensus.BatchingStateMachine, batching bool, es []coreconsensus.Entry, results []any) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = origin(coreconsensus.StateMachineFailed,
				errs.String("index", u64s(es[0].Position.Index)), errs.String("panic", fmt.Sprint(p)))
		}
	}()
	if batching {
		b.ApplyBatch(es, results)
		return nil
	}
	for i := range es {
		results[i] = n.cfg.Machine.Apply(es[i])
	}
	return nil
}

// applied1 records one applied entry and answers the Propose that created
// it on this node.
func (n *Node) applied1(e *coreconsensus.Entry, val any) {
	n.am.Lock()
	n.applied = e.Position.Index
	n.appliedTerm = e.Position.Term
	w := n.waiters[e.Position.Index]
	delete(n.waiters, e.Position.Index)
	if n.keepResults > 0 {
		n.results[e.Position.Index] = applyResult{term: e.Position.Term, val: val}
		n.resultOrder = append(n.resultOrder, e.Position.Index)
		for len(n.resultOrder) > 4096 {
			delete(n.results, n.resultOrder[0])
			n.resultOrder = n.resultOrder[1:]
		}
	}
	n.acond.Broadcast()
	n.am.Unlock()
	if w == nil {
		return
	}
	if w.local {
		n.pendingBytes.Add(-int64(len(w.entry.Data)))
	}
	tok := n.token(e.Position)
	if w.pos.Term != e.Position.Term {
		// Another entry is COMMITTED at its position: certainly not
		// committed (R7-15).
		w.done <- propResult{err: withToken(coreconsensus.EntrySuperseded, tok)}
		return
	}
	w.done <- propResult{tok: tok, val: val}
}

// waitApplied blocks until index is applied, the node stops, or done closes.
func (n *Node) waitApplied(done <-chan struct{}, index uint64) bool {
	stopWake := make(chan struct{})
	defer close(stopWake)
	go func() {
		select {
		case <-done:
		case <-n.loopDone:
		case <-stopWake:
			return
		}
		n.am.Lock()
		n.acond.Broadcast()
		n.am.Unlock()
	}()
	n.am.Lock()
	defer n.am.Unlock()
	for n.applied < index {
		select {
		case <-done:
			return false
		case <-n.loopDone:
			return false
		default:
		}
		n.acond.Wait()
	}
	return true
}
