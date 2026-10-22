package consensus

import (
	"context"
	"errors"
	"fmt"
	"io"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Latest opens the newest complete snapshot of a store (R7-07).
func Latest(s coreconsensus.SnapshotStore) (coreconsensus.SnapshotHandle, error) {
	metas, err := s.List()
	if err != nil {
		return nil, err
	}
	if len(metas) == 0 {
		return nil, origin(coreconsensus.SnapshotCorrupt, errs.String("why", "no snapshot"))
	}
	return s.Open(metas[0].ID)
}

// restoreFrom verifies a snapshot's digest, then restores the state machine
// from it, serialised with Apply.
func (n *Node) restoreFrom(m coreconsensus.SnapshotMetaValue, durIdx uint64) (err error) {
	h, err := n.cfg.Snapshots.Open(m.ID)
	if err != nil {
		return err
	}
	defer func() {
		// The handle is only read; its Close matters to the node only when
		// it is the one failure there was.
		if cerr := h.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	if err := verifySnapshot(io.NewSectionReader(h, 0, m.EncodedSize), m, n.cfg.SnapshotCodec, n.cfg.MaxSnapshotBytes); err != nil {
		return err
	}
	if durIdx < m.Position.Index {
		cr := newChunkReader(io.NewSectionReader(h, 0, m.EncodedSize), n.cfg.SnapshotCodec, max(m.ChunkSize, 1), n.cfg.MaxSnapshotBytes)
		if err := n.cfg.Machine.Restore(cr, m); err != nil {
			return err
		}
		n.am.Lock()
		n.applied, n.appliedTerm = m.Position.Index, m.Position.Term
		n.answerCovered(m.Position.Index)
		n.acond.Broadcast()
		n.am.Unlock()
	}
	n.snapState.mu.Lock()
	n.snapState.last = max(n.snapState.last, m.Position.Index)
	n.snapState.mu.Unlock()
	return nil
}

// maybeSnapshot cuts a snapshot after SnapshotEvery applied entries, on the
// apply goroutine; the write runs off it unless the machine cannot cut
// cheaply (D7). A trigger during a cut in flight is coalesced, not dropped:
// the cut is made once that one is written (cutPending), or a node that
// applies nothing more would keep the tail uncut and its log uncompacted.
func (n *Node) maybeSnapshot() {
	n.am.Lock()
	idx, term := n.applied, n.appliedTerm
	n.am.Unlock()
	ss := &n.snapState
	ss.mu.Lock()
	if idx < ss.last+n.cfg.SnapshotEvery {
		ss.mu.Unlock()
		return
	}
	if ss.inFlight {
		ss.pending = true
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

// cutFailed is what a snapshot cut that published nothing does to the node.
// A STORAGE_FAILED stops it: a failed write or sync of a snapshot is a
// failed write or sync of this node's disk, and D4 never retries one, for
// the page cache may now lie about the log on the same disk as well. Any
// other failure — the machine's Snapshot, its encoding — is logged, and the
// next cut tries again.
func (n *Node) cutFailed(err error) {
	if errs.HasCode(err, coreconsensus.CodeStorageFailed) {
		n.stop(err, true)
		return
	}
	n.obs.warn("consensus: snapshot cut failed", lstr("error", err.Error()))
}

// metaAt builds, on the event loop, the metadata of a snapshot at idx: the
// configuration, chain tip, timing, versions and no-space alarm in force
// there. The engine state is the one the committed entries through idx left
// (raft's EngineAt), never the one standing now: the engine applies its
// entries at commit, ahead of the applied index a cut is made at, and a
// node that installed the snapshot and was then truncated back would carry
// a contract whose entry it never applied (#402). It reports false when the
// node stopped before the loop could answer.
func (n *Node) metaAt(idx, term uint64) (coreconsensus.SnapshotMetaValue, bool) {
	var meta coreconsensus.SnapshotMetaValue
	ok := n.exec(func() {
		eng := n.r.EngineAt(idx)
		conf := n.r.ConfAt(idx)
		conf.Index = idx
		meta = coreconsensus.SnapshotMetaValue{
			ID: fmt.Sprintf("%020d-%020d", idx, term), Version: 1,
			Position: coreconsensus.PositionValue{Term: term, Index: idx}, Log: n.lineage(), Membership: conf,
			ChainTip: n.r.ChainAt(idx), Codec: string(n.cfg.SnapshotCodec.Algorithm()), ChunkSize: defaultChunkSize,
			Timing: eng.Timing, ClusterVersion: eng.Cluster, ClusterGeneration: eng.ClusterG, NoSpace: eng.NoSpace,
		}
	})
	return meta, ok
}

// cut captures the state machine at the applied index and writes it. When w
// is not nil, the encoded stream is copied to it too (SaveSnapshot). Unless
// the machine pauses apply to cut, or w is set, the write runs on a
// background goroutine counted in n.wg, which ends when the write does, so
// Close waits for it; its failure reaches cutFailed instead of the caller.
func (n *Node) cut(idx, term uint64, w io.Writer) error {
	meta, ok := n.metaAt(idx, term)
	if !ok {
		return n.stoppedErr()
	}
	snap, err := n.cfg.Machine.Snapshot()
	if err != nil {
		return err
	}
	if n.cfg.SnapshotPausesApply || w != nil {
		return n.writeSnapshot(snap, meta, w)
	}
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		if err := n.writeSnapshot(snap, meta, nil); err != nil {
			n.cutFailed(err)
			return
		}
		n.cutPending()
	}()
	return nil
}

// cutPending asks the apply goroutine for the cut a trigger coalesced while
// the one just written was in flight (D7). A failed cut asks for none: the
// next trigger tries again, as cutFailed says.
func (n *Node) cutPending() {
	ss := &n.snapState
	ss.mu.Lock()
	pending := ss.pending
	ss.pending = false
	ss.mu.Unlock()
	if pending && !n.stopped.Load() {
		n.aq.push(applyItem{cut: true})
	}
}

// writeSnapshot encodes a captured snapshot into a fresh sink, discarding a
// stale spool of the same id first, and commits it; on any failure the sink
// is aborted and nothing is published. It always releases the capture and
// clears the in-flight flag, so the next cut can run. A failed write clears
// the coalesced trigger with it, under the same lock: the next trigger tries
// again (cutFailed), and only one that falls after the flag is cleared
// survives.
func (n *Node) writeSnapshot(snap coreconsensus.Snapshot, meta coreconsensus.SnapshotMetaValue, extra io.Writer) error {
	defer snap.Release()
	err := n.publishSnapshot(snap, meta, extra)
	state := &n.snapState
	state.mu.Lock()
	state.inFlight = false
	if err != nil {
		state.pending = false
	}
	state.mu.Unlock()
	return err
}

// publishSnapshot is writeSnapshot's write: the sink, the encoded stream,
// the commit, then retention and compaction (afterCut).
func (n *Node) publishSnapshot(snap coreconsensus.Snapshot, meta coreconsensus.SnapshotMetaValue, extra io.Writer) error {
	state := &n.snapState
	sink, spooled, err := n.cfg.Snapshots.Create(meta.ID, meta)
	if err != nil {
		return err
	}
	if spooled > 0 {
		// A stale spool of that id: discarded, or the fresh cut would
		// resume from it.
		if err := sink.Abort(); err != nil {
			return err
		}
		if sink, _, err = n.cfg.Snapshots.Create(meta.ID, meta); err != nil {
			return err
		}
	}
	cutStart := n.clk.Now()
	var out io.Writer = sink
	if extra != nil {
		out = io.MultiWriter(sink, extra)
	}
	cw := newChunkWriter(out, n.cfg.SnapshotCodec, defaultChunkSize)
	if _, err := snap.WriteTo(cw); err != nil {
		return alsoFailed(err, "abort", sink.Abort())
	}
	if err := cw.Close(); err != nil {
		return alsoFailed(err, "abort", sink.Abort())
	}
	meta.EncodedSize, meta.DecodedSize = cw.encoded, cw.decoded
	copy(meta.SHA256[:], cw.sum.Sum(nil))
	if err := sink.Commit(meta); err != nil {
		return err
	}
	n.obs.record(mSnapshotDuration, n.clk.Since(cutStart).Seconds())
	n.obs.record(mSnapshotBytes, float64(meta.EncodedSize))
	n.obs.info("consensus: snapshot cut", lattr("index", meta.Position.Index), lattr("bytes", uint64(meta.EncodedSize)))
	state.mu.Lock()
	state.last = max(state.last, meta.Position.Index)
	state.mu.Unlock()
	n.afterCut()
	return nil
}

// afterCut keeps the two newest snapshots and compacts the log back to the
// older one, less SnapshotTrailing, with saturating arithmetic (D7).
func (n *Node) afterCut() {
	metas := n.retain()
	if len(metas) == 0 {
		return
	}
	older := metas[min(1, len(metas)-1)].Position.Index
	n.am.Lock()
	applied := n.applied
	n.am.Unlock()
	through := min(older, applied)
	if through <= n.cfg.SnapshotTrailing {
		return
	}
	through -= n.cfg.SnapshotTrailing
	n.exec(func() {
		n.r.Compact(through)
		n.handleReady()
	})
}

// retain keeps the two newest snapshots (D7, R6-02), removing the older ones,
// and returns the store's list, newest first; nil when it cannot be read. A
// cut publishes a snapshot and so does an install: both end here, or an
// install onto a node holding two left three until that node's next cut
// (#304).
func (n *Node) retain() []coreconsensus.SnapshotMetaValue {
	metas, err := n.cfg.Snapshots.List()
	if err != nil {
		return nil
	}
	n.removeOld(metas[min(2, len(metas)):])
	return metas
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

// requestCut asks the apply goroutine for a cut, unless one is in flight.
func (n *Node) requestCut() {
	n.snapState.mu.Lock()
	busy := n.snapState.inFlight
	n.snapState.mu.Unlock()
	if !busy {
		n.aq.push(applyItem{cut: true})
	}
}

// SaveSnapshot cuts a snapshot now and copies its encoded stream to w.
func (n *Node) SaveSnapshot(ctx context.Context, op coreconsensus.Operator, w io.Writer) (coreconsensus.SnapshotMetaValue, error) {
	if !op.Valid() {
		return coreconsensus.SnapshotMetaValue{}, origin(coreconsensus.MembershipInvalid, errs.String("why", "operator"))
	}
	if _, err := n.Barrier(ctx); err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	ss := &n.snapState
	ss.mu.Lock()
	if ss.inFlight {
		ss.mu.Unlock()
		return coreconsensus.SnapshotMetaValue{}, origin(coreconsensus.Overloaded, errs.String("why", "a cut is in flight"))
	}
	ss.inFlight = true
	ss.mu.Unlock()
	// The cut runs on the apply goroutine, serialised with Apply.
	// done has one slot, so the apply goroutine never blocks on a caller that left.
	req := &saveReq{w: w, op: op, done: make(chan saveRes, 1)}
	n.aq.push(applyItem{save: req})
	select {
	case r := <-req.done:
		return r.meta, r.err
	case <-ctx.Done():
		return coreconsensus.SnapshotMetaValue{}, ctx.Err()
	case <-n.loopDone:
		return coreconsensus.SnapshotMetaValue{}, n.stoppedErr()
	}
}

// save runs a SaveSnapshot on the apply goroutine.
func (n *Node) save(req *saveReq) {
	n.am.Lock()
	idx, term := n.applied, n.appliedTerm
	n.am.Unlock()
	err := n.cut(idx, term, req.w)
	var m coreconsensus.SnapshotMetaValue
	if err == nil {
		// The caller keeps this metadata to restore from later: a list that
		// could not be read fails the save rather than answer a zero one.
		var metas []coreconsensus.SnapshotMetaValue
		if metas, err = n.cfg.Snapshots.List(); err == nil {
			for _, x := range metas {
				if x.Position.Index == idx {
					m = x
				}
			}
		}
	} else {
		n.snapState.mu.Lock()
		n.snapState.inFlight = false
		n.snapState.mu.Unlock()
		// The operator who asked reads the error too, but a storage failure
		// is the node's before it is the caller's.
		n.cutFailed(err)
	}
	if err == nil {
		// An administrative operation is recorded under its operator, as
		// the offline ones are in the audit file (D17).
		n.obs.info("consensus: snapshot saved", lstr("operator", string(req.op)), lattr("index", idx))
	}
	req.done <- saveRes{m, err}
}

// ---- sending ----

// snapshotToSend is the newest snapshot, when a peer may install it: none
// when this leader never cut one, and none when the newest is of the
// history before a Recover, or below the index it kept — the entries kept
// after it would reach the peer by append and be applied there under the
// new identity, not the predecessor's (#507). A cut of this lineage, at or
// past the kept index, names both.
func (n *Node) snapshotToSend() (coreconsensus.SnapshotMetaValue, bool) {
	metas, err := n.cfg.Snapshots.List()
	if err != nil || len(metas) == 0 {
		return coreconsensus.SnapshotMetaValue{}, false
	}
	l := n.lineage()
	if !metas[0].Log.Same(l) || metas[0].Position.Index < l.KeptIndex {
		return coreconsensus.SnapshotMetaValue{}, false
	}
	return metas[0], true
}

// sendSnapshot streams the newest snapshot to a peer that needs one, at
// most one transfer per peer, and asks for a cut when there is none to
// send. It runs on the loop and launches the transfer on a background
// goroutine counted in n.wg, which ends when Stream returns, and a watcher
// goroutine that cancels the stream when the node stops and ends with it;
// the outcome goes back to raft on the loop.
func (n *Node) sendSnapshot(to coreconsensus.NodeID) {
	if n.sending[to] {
		return
	}
	meta, ok := n.snapshotToSend()
	if !ok {
		// None to send: cut one now, and the next probe sends it.
		n.requestCut()
		n.r.SnapshotDone(to, false)
		return
	}
	h, err := n.cfg.Snapshots.Open(meta.ID)
	if err != nil {
		n.r.SnapshotDone(to, false)
		return
	}
	n.sending[to] = true
	offer := coreconsensus.InstallOfferValue{Term: n.r.Status().Term, SnapshotID: meta.ID, Meta: meta}
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-n.stopc:
				cancel()
			case <-ctx.Done():
			}
		}()
		// The handle pins the snapshot until the transfer completes, so
		// compaction cannot delete it mid-transfer (R3-16).
		err := n.cfg.Transport.Stream(ctx, to, offer, func(off int64) (io.ReadCloser, error) {
			if off < 0 || off > meta.EncodedSize {
				return nil, origin(coreconsensus.FrameInvalid, errs.Int64("offset", off))
			}
			return io.NopCloser(io.NewSectionReader(h, off, meta.EncodedSize-off)), nil
		})
		cancel()
		if cerr := h.Close(); cerr != nil {
			// The transfer is over whatever the read handle says; the pin
			// it held is released either way.
			n.obs.warn("consensus: snapshot handle close failed", lstr("id", meta.ID), lstr("error", cerr.Error()))
		}
		if errors.Is(err, coreconsensus.Compacted) {
			// The peer's log starts past this snapshot: cut a newer one,
			// which the next probe sends.
			n.requestCut()
		}
		n.exec(func() {
			delete(n.sending, to)
			n.r.SnapshotDone(to, err == nil)
			n.handleReady()
		})
	}()
}
