package consensus

import (
	"io"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// offer answers a peer's snapshot offer: held when this node already covers
// it, refused with the code a leader can act on, or accepted at the offset
// a resumed spool already holds. It is judged on the event loop, and a node
// that stopped before the loop answered refuses with NODE_STOPPED.
func (n *Node) offer(from coreconsensus.AuthenticatedPeerValue, o coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue {
	acc := coreconsensus.InstallAcceptValue{SnapshotID: o.SnapshotID}
	ok := n.exec(func() { n.judgeOffer(from.ID, &o, &acc) })
	if !ok {
		acc.Refused = coreconsensus.CodeNodeStopped
	}
	return acc
}

// judgeOffer is offer's verdict, on the loop: screened first, then answered
// from a copy already held, else a sink is opened and kept for install.
func (n *Node) judgeOffer(from coreconsensus.NodeID, o *coreconsensus.InstallOfferValue, acc *coreconsensus.InstallAcceptValue) {
	if n.screenOffer(from, o, acc) || n.answerFromHeld(o, acc) {
		return
	}
	sink, off, err := n.cfg.Snapshots.Create(o.SnapshotID, o.Meta)
	if err != nil {
		acc.Refused = coreconsensus.CodeStorageFailed
		return
	}
	n.keepSpool(o.SnapshotID, sink)
	acc.Offset = off
}

// screenOffer settles an offer raft or the bounds decide on their own, and
// reports whether it did: one this node already covers is held, and one
// from a stale term, of another version or over MaxSnapshotBytes is refused
// before any byte is spooled.
func (n *Node) screenOffer(from coreconsensus.NodeID, o *coreconsensus.InstallOfferValue, acc *coreconsensus.InstallAcceptValue) bool {
	code := n.r.CheckOffer(from, *o, n.r.NoTable())
	if code == coreconsensus.CodeAdminPreconditionFailed {
		// The receiver already covers the offered index — it holds that
		// snapshot, or committed past it: an idempotent success, and the
		// leader moves the peer back to probe (D7, R3-16).
		acc.Held = true
		return true
	}
	if code != 0 {
		acc.Refused = code
		return true
	}
	if o.SnapshotID != o.Meta.ID || o.Meta.Version != 1 {
		acc.Refused = coreconsensus.CodeVersionUnsupported
		return true
	}
	if !lineageAccepts(n.lineage(), o.Meta.Log, o.Meta.Position.Index) {
		// A snapshot of another history: the entries after it would be
		// applied under an identity it does not continue (#507). A leader
		// never offers one; it cuts a snapshot of its own lineage instead.
		acc.Refused = coreconsensus.CodeLineageMismatch
		return true
	}
	if o.Meta.DecodedSize > n.cfg.MaxSnapshotBytes || o.Meta.EncodedSize > n.cfg.MaxSnapshotBytes {
		acc.Refused = coreconsensus.CodeSnapshotTooLarge
		return true
	}
	return false
}

// answerFromHeld answers an offer for a snapshot id this node already
// stores, and reports whether it did: held, unless the node has no table,
// in which case its copy failed its checksums and is removed so the offer
// proceeds and replaces it.
func (n *Node) answerFromHeld(o *coreconsensus.InstallOfferValue, acc *coreconsensus.InstallAcceptValue) bool {
	metas, err := n.cfg.Snapshots.List()
	if err != nil {
		acc.Refused = coreconsensus.CodeStorageFailed
		return true
	}
	for _, m := range metas {
		if m.ID != o.SnapshotID {
			continue
		}
		if !n.r.NoTable() {
			acc.Held = true
			return true
		}
		// A node with no table holds that id only as a copy that failed
		// its checksums: it is replaced, never answered as held.
		if err := n.cfg.Snapshots.Remove(m.ID); err != nil {
			acc.Refused = coreconsensus.CodeStorageFailed
			return true
		}
	}
	return false
}

// keepSpool holds the sink an accepted offer opened until its body
// arrives. A sink an earlier offer of the same id left open is released,
// never aborted: its spool is what the new offer resumes (review of PR
// #301, L4).
func (n *Node) keepSpool(id string, sink coreconsensus.SnapshotSink) {
	n.snapState.mu.Lock()
	defer n.snapState.mu.Unlock()
	if n.snapState.spools == nil {
		n.snapState.spools = map[string]coreconsensus.SnapshotSink{}
	}
	if old := n.snapState.spools[id]; old != nil {
		if r, ok := old.(interface{ release() }); ok {
			r.release()
		}
	}
	n.snapState.spools[id] = sink
}

// install receives the stream of an accepted offer into its spool, commits
// it, and has the apply goroutine restore from it before raft adopts it. A
// short stream keeps the spool for the next offer to resume; a snapshot
// that does not restore is removed, so a restart does not load it. The
// test-only crash hook launches HardClose on its own goroutine, which ends
// when HardClose returns, because install itself runs under the transport.
func (n *Node) install(req coreconsensus.InstallParams) error {
	snapID := req.Offer.SnapshotID
	n.snapState.mu.Lock()
	sink := n.snapState.spools[snapID]
	delete(n.snapState.spools, snapID)
	n.snapState.mu.Unlock()
	if sink == nil {
		return origin(coreconsensus.SnapshotCorrupt, errs.String("why", "no accepted offer"))
	}
	meta := req.Offer.Meta
	want := meta.EncodedSize - req.Offset
	k, err := io.Copy(sink, io.LimitReader(req.Body, want+1))
	if err != nil || k != want {
		// The spool stays: the next offer resumes from its last chunk.
		return snapshotCorrupt("stream ended early")
	}
	// Freshness again, on the event loop, right before the install is
	// accepted (D7): the receiver may have committed past the snapshot
	// through a new leader's appends while the body streamed (review H3).
	var code errs.Code
	var adoptErr error
	if !n.exec(func() {
		if code = n.r.CheckOffer(req.From.ID, req.Offer, n.r.NoTable()); code == 0 {
			// The lineage the snapshot records is durable before the
			// restore, and so before any entry it covers is applied (#507).
			adoptErr = n.adoptLineage(meta.Log)
		}
	}) {
		return n.stoppedErr()
	}
	if adoptErr != nil {
		abandon(sink.Abort())
		n.stop(adoptErr, true)
		return adoptErr
	}
	if code != 0 {
		// The spool is discarded either way: the refusal is the answer the
		// leader acts on, and nothing here could act on a failed Abort.
		abandon(sink.Abort())
		return origin(coreconsensus.AdminPreconditionFailed, errs.String("why", "the offer is no longer fresh"))
	}
	if err := sink.Commit(meta); err != nil {
		if errs.HasCode(err, coreconsensus.CodeStorageFailed) {
			// The spool's fsync, rename or .meta failed: this node's disk,
			// as a cut's, and D4 never retries one. It used to wait for the
			// next offer and keep voting and appending on it (#404).
			n.stop(err, true)
		}
		return err
	}
	if n.crashAfterPublish.Load() {
		// Test-only: a crash right after the commit point of the install.
		go n.HardClose()
		return n.stoppedErr()
	}
	done := make(chan error, 1)
	n.aq.push(applyItem{restore: &restoreReq{meta: meta, done: done}})
	select {
	case err = <-done:
	case <-n.stopc:
		return n.stoppedErr()
	}
	if err != nil {
		// The installed copy did not restore: unpublished, so a restart
		// does not load it again, or the failure to do so travels with it.
		return alsoFailed(err, "remove", n.cfg.Snapshots.Remove(snapID))
	}
	n.installed(meta, req.From.ID)
	return nil
}

// installed moves raft onto a restored install and keeps the two newest
// snapshots: the installed one is the newest, and the oldest of the ones
// this node held goes, as after a cut.
func (n *Node) installed(meta coreconsensus.SnapshotMetaValue, from coreconsensus.NodeID) {
	n.exec(func() {
		n.r.InstallSnapshot(meta)
		n.handleReady()
	})
	n.retain()
	n.obs.inc(mSnapshotInstalls)
	n.obs.info("consensus: snapshot installed", lattr("index", meta.Position.Index), lattr("from", uint64(from)))
}
