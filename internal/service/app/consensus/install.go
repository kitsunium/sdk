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
	if acc.Refused == coreconsensus.CodeVersionUnsupported {
		// A snapshot version this node does not know stops it (D18): only
		// refused, it was offered again at every probe while the node
		// stayed out of replication and reported ready (#513). The stop
		// is off the loop, which never waits on the marker's write.
		n.stop(origin(coreconsensus.VersionUnsupported, errs.Int("snapshot_version", int(o.Meta.Version)),
			errs.String("snapshot_codec", o.Meta.Codec)), false)
	}
	return acc
}

// judgeOffer is offer's verdict, on the loop: screened first, then answered
// from a copy already held, else a sink is opened and kept for install.
func (n *Node) judgeOffer(from coreconsensus.NodeID, o *coreconsensus.InstallOfferValue, acc *coreconsensus.InstallAcceptValue) {
	if n.screenOffer(from, o, acc) || n.answerFromHeld(o, acc) {
		return
	}
	n.dropOtherSpools(o.SnapshotID)
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
	if o.SnapshotID != o.Meta.ID || termless(&o.Meta) || !idNamesPosition(&o.Meta) {
		// An offer at term 0 above index 0 names no committed entry:
		// installed, it stopped this node at the compaction (#544). One
		// whose id does not name its position carries a metadata that
		// rotted (#546).
		acc.Refused = coreconsensus.CodeFrameInvalid
		return true
	}
	if _, err := n.codecOf(&o.Meta); err != nil {
		// A metadata version or a codec this build does not know: offer
		// stops the node on this refusal (D18, #513).
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
// in which case its copy failed its checksums and the offer proceeds and
// replaces it, or the copy lies past what the node applied: published and
// never restored, it is not the node's state, and answered held the leader
// probed past it, the append was refused and the offer came again for as
// long as it stayed the leader's newest (#528). The copy is not removed first: the commit of the replacement
// overwrites it, and until then it is what the node's members are read
// from. Removed, a replacement that then failed — a transfer cut short, a
// Restore refused — left the node no snapshot, and at its next start a log
// that begins past a compaction with nothing naming the members: it
// admitted nobody, for good (#526).
func (n *Node) answerFromHeld(o *coreconsensus.InstallOfferValue, acc *coreconsensus.InstallAcceptValue) bool {
	metas, err := n.cfg.Snapshots.List()
	if err != nil {
		acc.Refused = coreconsensus.CodeStorageFailed
		return true
	}
	n.am.Lock()
	applied := n.applied
	n.am.Unlock()
	for _, m := range metas {
		if m.ID == o.SnapshotID && !n.r.NoTable() && m.Position.Index <= applied {
			acc.Held = true
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
		releaseSink(old)
	}
	n.snapState.spools[id] = sink
}

// releaseSink closes a sink's handle and keeps its spool for a resume,
// when the store's sink can; a sink that cannot keeps nothing open.
func releaseSink(sink coreconsensus.SnapshotSink) {
	if r, ok := sink.(interface{ release() }); ok {
		r.release()
	}
}

// dropOtherSpools aborts the sinks earlier offers of another id opened and
// whose body never came: an offer of id supersedes them, and kept, each
// held its spool on the data disk for good (#527).
func (n *Node) dropOtherSpools(id string) {
	n.snapState.mu.Lock()
	var stale []coreconsensus.SnapshotSink
	for other, sink := range n.snapState.spools {
		if other != id {
			stale = append(stale, sink)
			delete(n.snapState.spools, other)
		}
	}
	n.snapState.mu.Unlock()
	for _, sink := range stale {
		// A spool that cannot be removed now is swept by the next Create
		// or the next open; nothing here could act on the failure.
		abandon(sink.Abort())
	}
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
		// The spool stays: the next offer resumes from its last chunk. Its
		// handle goes, or every broken transfer leaked a descriptor and
		// kept the spool counted as written to (#527).
		releaseSink(sink)
		return snapshotCorrupt("stream ended early")
	}
	if err := n.stillFresh(&req, sink); err != nil {
		return err
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
		return n.restoreFailed(snapID, err)
	}
	n.installed(meta, req.From.ID)
	return nil
}

// restoreFailed is what an installed copy that did not restore does to the
// node. It is unpublished, so a restart does not load it again, or the
// failure to do so travels with it — unless the node has no table: its
// configuration is read from its snapshots' metadata, and a node left none
// starts with no member to admit (#526). The copy stays until another is
// published over it, and a restart falls back to no table again.
func (n *Node) restoreFailed(snapID string, err error) error {
	if errs.HasCode(err, coreconsensus.CodeVersionUnsupported) {
		// A state this build cannot read: unknown means stop, never skip
		// (D18). Removed and answered as a failure, it was streamed again
		// at every probe and the node left the quorum while it reported
		// ready (#513). The copy stays, and a restart refuses to open on it.
		n.stop(err, false)
		return err
	}
	noTable := false
	if !n.exec(func() { noTable = n.r.NoTable() }) {
		return err
	}
	if noTable {
		n.obs.warn("consensus: an installed snapshot did not restore", lstr("id", snapID), lstr("error", err.Error()))
		return err
	}
	return alsoFailed(err, "remove", n.cfg.Snapshots.Remove(snapID))
}

// stillFresh judges the offer again, on the event loop, right before the
// install is accepted (D7): the receiver may have committed past the
// snapshot through a new leader's appends while the body streamed (review
// H3). A fresh one has the lineage it records adopted there, durably,
// before the restore and so before any entry it covers is applied (#507).
// A stale one, or a failed adoption, discards the spool; the second stops
// the node, as every failed write of its disk.
func (n *Node) stillFresh(req *coreconsensus.InstallParams, sink coreconsensus.SnapshotSink) error {
	var code errs.Code
	var adoptErr error
	if !n.exec(func() {
		if code = n.r.CheckOffer(req.From.ID, req.Offer, n.r.NoTable()); code == 0 {
			adoptErr = n.adoptLineage(req.Offer.Meta.Log)
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
