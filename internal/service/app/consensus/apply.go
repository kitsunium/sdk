package consensus

import (
	"fmt"
	"io"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

type applyItem struct {
	entries []coreconsensus.EntryValue
	restore *restoreReq
	save    *saveReq
	cut     bool
}

type restoreReq struct {
	meta coreconsensus.SnapshotMetaValue
	done chan error
}

type saveReq struct {
	w io.Writer
	// op is the operator the save is recorded under.
	op   coreconsensus.Operator
	done chan saveRes
}

type saveRes struct {
	meta coreconsensus.SnapshotMetaValue
	err  error
}

// token builds an entry's lineage-scoped command identity: an entry kept by
// Recover keeps its PREDECESSOR identity (D10, R7-09).
func (n *Node) token(p coreconsensus.PositionValue) coreconsensus.TokenValue {
	l := n.ident
	if l.Predecessor != (coreconsensus.ClusterID{}) && p.Index <= l.KeptIndex {
		return coreconsensus.TokenValue{Cluster: l.Predecessor, Epoch: l.PredecessorEpoch, Term: p.Term, Index: p.Index}
	}
	return coreconsensus.TokenValue{Cluster: l.Cluster, Epoch: l.Epoch, Term: p.Term, Index: p.Index}
}

// applyLoop is the apply goroutine: every committed command in log order,
// on one goroutine (D3).
func (n *Node) applyLoop() {
	defer n.wg.Done()
	var items []applyItem
	for {
		select {
		case _, ok := <-n.aq.signal:
			// The fifo never closes its signal channel; were it closed, the
			// queue would have no producer left, and the goroutine ends as
			// it does on stop rather than spinning on the closed channel.
			if !ok {
				return
			}
		case <-n.stopc:
			return
		}
		items = n.aq.take(items)
		if !n.applyItems(items) {
			return
		}
	}
}

// applyItems runs one batch taken from the apply queue in order. It returns
// false when the apply goroutine must end: the node stopped, or applying an
// entry stopped it.
func (n *Node) applyItems(items []applyItem) bool {
	for _, it := range items {
		if n.stopped.Load() {
			return false
		}
		if it.restore != nil {
			// A machine already past the snapshot is never rolled back:
			// the entries it applied after the snapshot are committed
			// and would not be applied again (review H3).
			n.am.Lock()
			applied := n.applied
			n.am.Unlock()
			it.restore.done <- n.restoreFrom(it.restore.meta, applied)
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
			return false
		}
		n.maybeSnapshot()
	}
	return true
}

// applyEntries applies committed entries in log order, skipping those a
// restored snapshot already covers and handing a run of commands to a
// BatchingStateMachine in one call. It returns false once the state machine
// failed and the node is stopping: nothing after that entry is applied.
func (n *Node) applyEntries(es []coreconsensus.EntryValue) bool {
	batcher, batching := n.cfg.Machine.(coreconsensus.BatchingStateMachine)
	for i := 0; i < len(es); {
		e := &es[i]
		n.am.Lock()
		skip := e.Position.Index <= n.applied
		n.am.Unlock()
		n.applyLag.Add(-int64(len(e.Data)))
		if skip {
			i++
			continue
		}
		if !e.Kind.Known() {
			// Never skipped as a no-op: this version cannot apply it.
			n.stop(unsupportedKind(e.Kind, e.Position.Index), false)
			return false
		}
		if e.Kind != coreconsensus.EntryCommand {
			n.applied1(e, nil)
			i++
			continue
		}
		j, ok := n.applyCommands(batcher, batching, es, i)
		if !ok {
			return false
		}
		i = j
	}
	return true
}

// applyCommands applies the command at es[i], and with a batching machine
// every command that directly follows it, then answers their proposals. It
// returns the index of the first entry it did not apply, and false when the
// state machine failed and the node was stopped.
func (n *Node) applyCommands(batcher coreconsensus.BatchingStateMachine, batching bool, es []coreconsensus.EntryValue, i int) (int, bool) {
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
	applyStart := n.clk.Now()
	err := n.safeApply(batcher, batching, es[i:j], results)
	n.obs.record(mApplyLatency, n.clk.Since(applyStart).Seconds())
	if err != nil {
		n.stop(err, false)
		return j, false
	}
	for k := i; k < j; k++ {
		n.applied1(&es[k], results[k-i])
	}
	return j, true
}

// forceCut cuts a snapshot at the applied index whatever SnapshotEvery says:
// a cut requested by a peer that needs one (requestCut), or one a trigger
// coalesced (cutPending). Its failure goes through cutFailed like every other
// cut's: written here, on the apply goroutine, when the machine pauses apply
// to cut, a failed sync of the spool used to be dropped, and the node went on
// over a disk that had just failed an fsync (#404, D4).
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
		ss.inFlight, ss.pending = false, false
		ss.mu.Unlock()
		n.cutFailed(err)
	}
}

// safeApply calls the state machine; a panic stops the node with
// STATE_MACHINE_FAILED, and the entry IS committed (D3).
func (n *Node) safeApply(b coreconsensus.BatchingStateMachine, batching bool, es []coreconsensus.EntryValue, results []any) (err error) {
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

// removeOld removes the snapshots retain no longer keeps. One refused while
// a transfer reads it is kept until the next cut or install, which tries
// again: worth a line, never a stop. A failed unlink or directory sync is
// the disk, and stops the node as a cut's does (D4, #422).
func (n *Node) removeOld(old []coreconsensus.SnapshotMetaValue) {
	for _, m := range old {
		err := n.cfg.Snapshots.Remove(m.ID)
		if err == nil {
			continue
		}
		if errs.HasCode(err, coreconsensus.CodeStorageFailed) {
			n.cutFailed(err)
			return
		}
		n.obs.info("consensus: an old snapshot is kept for now", lstr("id", m.ID), lstr("error", err.Error()))
	}
}

// answerCovered answers every proposal waiting at or below index, which a
// restored snapshot now covers: applied1 never runs for those indexes, and
// sweepWaiters answers only above the commit, so they would wait forever
// and their bytes stay counted against MaxPendingBytes (#419). The snapshot
// holds their entry or another one at that index, which only a read
// through Barrier tells apart: OUTCOME_UNKNOWN. The caller holds am.
func (n *Node) answerCovered(index uint64) {
	for idx, p := range n.waiters {
		if idx > index {
			continue
		}
		delete(n.waiters, idx)
		if p.local {
			n.pendingBytes.Add(-int64(len(p.entry.Data)))
		}
		p.done <- propResult{err: outcomeUnknown("a snapshot was installed over its position")}
	}
}

// applied1 records one applied entry and answers the Propose that created
// it on this node.
func (n *Node) applied1(e *coreconsensus.EntryValue, val any) {
	n.am.Lock()
	n.applied = e.Position.Index
	n.appliedTerm = e.Position.Term
	n.logApplied.Store(e.Position.Index)
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
// It launches one goroutine that turns done or the loop's end into a
// broadcast on the apply condition, since a sync.Cond cannot select; that
// goroutine ends after that one broadcast or, through stopWake, as soon as
// waitApplied returns, so none accumulates across calls.
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

// unsupportedKind stops a node that meets an entry kind it does not know
// (review M3).
func unsupportedKind(k coreconsensus.EntryKind, index uint64) error {
	return origin(coreconsensus.VersionUnsupported, errs.Int("kind", int(k)), errs.String("index", u64s(index)))
}
