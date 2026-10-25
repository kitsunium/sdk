package consensus

import (
	"bytes"
	"fmt"
	"sync"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

type applyItem struct {
	entries []coreconsensus.EntryValue
	// read names committed entries to read back from the log and apply:
	// raft holds them without their payloads (D6, #664).
	read    raft.ReadBackSpec
	restore *restoreReq
	save    *saveReq
	cut     bool
	// compact compacts the log again once a pin on it ended (sendPin).
	compact bool
}

type restoreReq struct {
	meta coreconsensus.SnapshotMetaValue
	done chan error
}

type saveReq struct {
	// op is the operator the save is recorded under.
	op   coreconsensus.Operator
	done chan saveRes
	// mu orders the answer with the caller leaving: gone is set once the
	// caller left, and a handle answered after that is closed, not leaked.
	mu   sync.Mutex
	gone bool
}

type saveRes struct {
	meta coreconsensus.SnapshotMetaValue
	// h pins the saved snapshot until the caller copied it; nil on error.
	h   coreconsensus.SnapshotHandle
	err error
}

// answer hands res to the caller, or closes its handle when the caller
// already left. done has one slot and is answered once, so it never blocks
// the apply goroutine.
func (r *saveReq) answer(res *saveRes) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gone {
		closeSaved(res)
		return
	}
	r.done <- *res
}

// leave records that the caller left, and closes the handle of an answer
// already given.
func (r *saveReq) leave() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gone = true
	select {
	case res := <-r.done:
		closeSaved(&res)
	default:
	}
}

// closeSaved closes the handle of an answer no caller copies.
func closeSaved(res *saveRes) {
	if res.h != nil {
		abandon(res.h.Close())
	}
}

// token builds an entry's lineage-scoped command identity: an entry kept by
// Recover keeps its PREDECESSOR identity (D10, R7-09).
func (n *Node) token(p coreconsensus.PositionValue) coreconsensus.TokenValue {
	l := n.lineage()
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
	for k := range items {
		if n.stopped.Load() {
			return false
		}
		// The slot lets go of the item as it is taken: the batch taken from
		// the queue, and the slice reused for the next one, held the
		// payloads of the entries already applied past the apply lag that
		// released them, and raft handed more meanwhile (#664).
		it := items[k]
		items[k] = applyItem{}
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
		if it.compact {
			n.afterCut()
			continue
		}
		if !n.applyHanded(&it) {
			return false
		}
	}
	return true
}

// applyHanded applies what raft handed in one item, entries in memory or a
// read of the log, then wakes the loop when apply has room again. It
// returns false when the apply goroutine must end.
func (n *Node) applyHanded(it *applyItem) bool {
	ok := true
	if it.read.Lo != 0 {
		ok = n.applyRead(it.read)
		n.applyReads.Add(-1)
	} else if ok = n.applyEntries(it.entries); ok {
		n.maybeSnapshot(payloadBytes(it.entries))
	}
	if ok {
		n.applyMoved()
	}
	return ok
}

// applyRead applies the committed entries of rg, which raft handed by
// index, read back from the log a batch at a time: at most MaxBatchBytes
// and MaxApplyLagBytes, at least one entry, each batch counted in the
// apply lag until applied (D6, #664). Those a restored snapshot covers are
// not read: no compaction passes what was applied, and an install's comes
// after its restore, which this goroutine runs after this read. It returns
// false when the goroutine must end: the node stopped, or the log could not
// give back what it holds committed, which stops it.
func (n *Node) applyRead(rg raft.ReadBackSpec) bool {
	maxBytes := int(min(int64(n.cfg.MaxBatchBytes), n.cfg.MaxApplyLagBytes))
	for lo := rg.Lo; ; {
		if n.stopped.Load() {
			return false
		}
		n.am.Lock()
		lo = max(lo, n.applied+1)
		n.am.Unlock()
		if lo >= rg.Hi {
			return true
		}
		es, ok := n.readBack(lo, rg.Hi, maxBytes)
		if !ok || !n.applyEntries(es) {
			return false
		}
		n.maybeSnapshot(payloadBytes(es))
		lo = es[len(es)-1].Position.Index + 1
	}
}

// readBack reads the committed entries from lo on, below hi, maxBytes of
// payload at most but one entry, copied out of the store's buffers and
// counted in the apply lag. A log that cannot give them back cannot be
// trusted to agree with itself, and stops the node (D4): ok is false.
func (n *Node) readBack(lo, hi uint64, maxBytes int) (es []coreconsensus.EntryValue, ok bool) {
	es, err := n.cfg.Log.Entries(lo, hi, maxBytes, nil)
	if err == nil && !contiguousFrom(es, lo) {
		err = storageFailed("read back", errImpossible)
	}
	if err != nil {
		if !n.stopped.Load() {
			n.stop(err, true)
		}
		return nil, false
	}
	var b int64
	for i := range es {
		es[i].Data = bytes.Clone(es[i].Data)
		b += int64(len(es[i].Data))
	}
	n.obs.setGauge(mApplyLagBytes, float64(n.applyLag.Add(b)))
	return es, true
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
		if skip {
			n.applyLag.Add(-int64(len(e.Data)))
			i++
			continue
		}
		if !e.Kind.Known() {
			// Never skipped as a no-op: this version cannot apply it.
			n.stop(unsupportedKind(e.Kind, e.Position.Index), false)
			return false
		}
		if e.Kind != coreconsensus.EntryCommand {
			n.applyLag.Add(-int64(len(e.Data)))
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
	var held int64
	for k := i; k < j; k++ {
		es[k].Token = n.token(es[k].Position)
		held += int64(len(es[k].Data))
	}
	applyStart := n.clk.Now()
	err := n.safeApply(batcher, batching, es[i:j], results)
	n.obs.record(mApplyLatency, n.clk.Since(applyStart).Seconds())
	if err != nil {
		n.stop(err, false)
		return j, false
	}
	// Every command of the batch leaves the apply lag, once the machine
	// applied it and before applied1 publishes it: counted once per Ready
	// and released only for the head of each batch, their bytes stayed in
	// the lag for good, and the leader refused every proposal OVERLOADED once
	// it reached MaxApplyLagBytes (#514); released before the machine
	// applied them, raft handed another MaxApplyLagBytes while a batch was
	// still being applied (#664). They are summed before the call: a machine
	// may drop the payloads it is handed, and summed after, the lag kept
	// their bytes for good and raft handed this node nothing more.
	n.applyLag.Add(-held)
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
	if ss.inFlight || idx == 0 || idx <= ss.last || n.clk.Now().Before(ss.retryAt) {
		ss.mu.Unlock()
		return
	}
	ss.inFlight = true
	ss.mu.Unlock()
	if err := n.cut(idx, term, false); err != nil {
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
