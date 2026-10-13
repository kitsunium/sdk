package consensus

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// snapState tracks the cuts: at most one in flight, coalesced (D7).
type snapState struct {
	mu       sync.Mutex
	last     uint64
	inFlight bool
	spools   map[string]coreconsensus.SnapshotSink
}

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
func (n *Node) restoreFrom(m coreconsensus.SnapshotMeta, durIdx uint64) error {
	h, err := n.cfg.Snapshots.Open(m.ID)
	if err != nil {
		return err
	}
	defer h.Close()
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
// cheaply (D7).
func (n *Node) maybeSnapshot() {
	n.am.Lock()
	idx, term := n.applied, n.appliedTerm
	n.am.Unlock()
	ss := &n.snapState
	ss.mu.Lock()
	if ss.inFlight || idx < ss.last+n.cfg.SnapshotEvery {
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

func (n *Node) metaAt(idx, term uint64) (coreconsensus.SnapshotMeta, bool) {
	var meta coreconsensus.SnapshotMeta
	ok := n.exec(func() {
		st := n.r.Status()
		conf := n.r.ConfAt(idx)
		conf.Index = idx
		meta = coreconsensus.SnapshotMeta{
			ID: fmt.Sprintf("%020d-%020d", idx, term), Version: 1,
			Position: coreconsensus.Position{Term: term, Index: idx}, Log: n.ident, Membership: conf,
			ChainTip: n.r.ChainAt(idx), Codec: string(n.cfg.SnapshotCodec.Algorithm()), ChunkSize: defaultChunkSize,
			Timing: st.Timing, ClusterVersion: st.Cluster, ClusterGeneration: st.ClusterG, NoSpace: st.NoSpace,
		}
	})
	return meta, ok
}

// cut captures the state machine at the applied index and writes it. When w
// is not nil, the encoded stream is copied to it too (SaveSnapshot).
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
		_ = n.writeSnapshot(snap, meta, nil)
	}()
	return nil
}

func (n *Node) writeSnapshot(snap coreconsensus.Snapshot, meta coreconsensus.SnapshotMeta, extra io.Writer) error {
	defer snap.Release()
	ss := &n.snapState
	done := func() {
		ss.mu.Lock()
		ss.inFlight = false
		ss.mu.Unlock()
	}
	defer done()
	sink, spooled, err := n.cfg.Snapshots.Create(meta.ID, meta)
	if err != nil {
		return err
	}
	if spooled > 0 {
		_ = sink.Abort()
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
		_ = sink.Abort()
		return err
	}
	if err := cw.Close(); err != nil {
		_ = sink.Abort()
		return err
	}
	meta.EncodedSize, meta.DecodedSize = cw.encoded, cw.decoded
	copy(meta.SHA256[:], cw.sum.Sum(nil))
	if err := sink.Commit(meta); err != nil {
		return err
	}
	n.obs.record(mSnapshotDuration, n.clk.Since(cutStart).Seconds())
	n.obs.record(mSnapshotBytes, float64(meta.EncodedSize))
	n.obs.info("consensus: snapshot cut", lattr("index", meta.Position.Index), lattr("bytes", uint64(meta.EncodedSize)))
	ss.mu.Lock()
	ss.last = max(ss.last, meta.Position.Index)
	ss.mu.Unlock()
	n.afterCut()
	return nil
}

// afterCut keeps the two newest snapshots and compacts the log back to the
// older one, less SnapshotTrailing, with saturating arithmetic (D7).
func (n *Node) afterCut() {
	metas, err := n.cfg.Snapshots.List()
	if err != nil || len(metas) == 0 {
		return
	}
	for _, m := range metas[min(2, len(metas)):] {
		_ = n.cfg.Snapshots.Remove(m.ID) // refused while a transfer reads it
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
func (n *Node) SaveSnapshot(ctx context.Context, op coreconsensus.Operator, w io.Writer) (coreconsensus.SnapshotMeta, error) {
	if !op.Valid() {
		return coreconsensus.SnapshotMeta{}, origin(coreconsensus.MembershipInvalid, errs.String("why", "operator"))
	}
	if _, err := n.Barrier(ctx); err != nil {
		return coreconsensus.SnapshotMeta{}, err
	}
	ss := &n.snapState
	ss.mu.Lock()
	if ss.inFlight {
		ss.mu.Unlock()
		return coreconsensus.SnapshotMeta{}, origin(coreconsensus.Overloaded, errs.String("why", "a cut is in flight"))
	}
	ss.inFlight = true
	ss.mu.Unlock()
	// The cut runs on the apply goroutine, serialised with Apply.
	req := &saveReq{w: w, done: make(chan saveRes, 1)}
	n.aq.push(applyItem{save: req})
	select {
	case r := <-req.done:
		return r.meta, r.err
	case <-ctx.Done():
		return coreconsensus.SnapshotMeta{}, ctx.Err()
	case <-n.loopDone:
		return coreconsensus.SnapshotMeta{}, n.stoppedErr()
	}
}

type saveReq struct {
	w    io.Writer
	done chan saveRes
}

type saveRes struct {
	meta coreconsensus.SnapshotMeta
	err  error
}

// save runs a SaveSnapshot on the apply goroutine.
func (n *Node) save(req *saveReq) {
	n.am.Lock()
	idx, term := n.applied, n.appliedTerm
	n.am.Unlock()
	err := n.cut(idx, term, req.w)
	var m coreconsensus.SnapshotMeta
	if err == nil {
		metas, _ := n.cfg.Snapshots.List()
		for _, x := range metas {
			if x.Position.Index == idx {
				m = x
			}
		}
	} else {
		n.snapState.mu.Lock()
		n.snapState.inFlight = false
		n.snapState.mu.Unlock()
	}
	req.done <- saveRes{m, err}
}

// ---- sending ----

func (n *Node) sendSnapshot(to coreconsensus.NodeID) {
	if n.sending[to] {
		return
	}
	metas, err := n.cfg.Snapshots.List()
	if err != nil || len(metas) == 0 {
		// A peer asks for a snapshot this leader never cut: cut one now,
		// and the next probe sends it.
		n.requestCut()
		n.r.SnapshotDone(to, false)
		return
	}
	meta := metas[0]
	h, err := n.cfg.Snapshots.Open(meta.ID)
	if err != nil {
		n.r.SnapshotDone(to, false)
		return
	}
	n.sending[to] = true
	offer := coreconsensus.InstallOffer{Term: n.r.Status().Term, SnapshotID: meta.ID, Meta: meta}
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
		_ = h.Close()
		n.exec(func() {
			delete(n.sending, to)
			n.r.SnapshotDone(to, err == nil)
			n.handleReady()
		})
	}()
}

// ---- receiving ----

func (n *Node) offer(from coreconsensus.AuthenticatedPeer, o coreconsensus.InstallOffer) coreconsensus.InstallAccept {
	acc := coreconsensus.InstallAccept{SnapshotID: o.SnapshotID}
	ok := n.exec(func() {
		code := n.r.CheckOffer(from.ID, o, n.r.NoTable())
		if code == coreconsensus.CodeAdminPreconditionFailed {
			// The receiver already covers the offered index — it holds that
			// snapshot, or committed past it: an idempotent success, and the
			// leader moves the peer back to probe (D7, R3-16).
			acc.Held = true
			return
		}
		if code != 0 {
			acc.Refused = code
			return
		}
		if o.SnapshotID != o.Meta.ID || o.Meta.Version != 1 {
			acc.Refused = coreconsensus.CodeVersionUnsupported
			return
		}
		if o.Meta.DecodedSize > n.cfg.MaxSnapshotBytes || o.Meta.EncodedSize > n.cfg.MaxSnapshotBytes {
			acc.Refused = coreconsensus.CodeSnapshotTooLarge
			return
		}
		metas, err := n.cfg.Snapshots.List()
		if err != nil {
			acc.Refused = coreconsensus.CodeStorageFailed
			return
		}
		for _, m := range metas {
			if m.ID != o.SnapshotID {
				continue
			}
			if !n.r.NoTable() {
				acc.Held = true
				return
			}
			// A node with no table holds that id only as a copy that failed
			// its checksums: it is replaced, never answered as held.
			if err := n.cfg.Snapshots.Remove(m.ID); err != nil {
				acc.Refused = coreconsensus.CodeStorageFailed
				return
			}
		}
		sink, off, err := n.cfg.Snapshots.Create(o.SnapshotID, o.Meta)
		if err != nil {
			acc.Refused = coreconsensus.CodeStorageFailed
			return
		}
		n.snapState.mu.Lock()
		if n.snapState.spools == nil {
			n.snapState.spools = map[string]coreconsensus.SnapshotSink{}
		}
		n.snapState.spools[o.SnapshotID] = sink
		n.snapState.mu.Unlock()
		acc.Offset = off
	})
	if !ok {
		acc.Refused = coreconsensus.CodeNodeStopped
	}
	return acc
}

func (n *Node) install(req coreconsensus.InstallRequest) error {
	id := req.Offer.SnapshotID
	n.snapState.mu.Lock()
	sink := n.snapState.spools[id]
	delete(n.snapState.spools, id)
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
	if err := sink.Commit(meta); err != nil {
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
		_ = n.cfg.Snapshots.Remove(id)
		return err
	}
	n.exec(func() {
		n.r.InstallSnapshot(meta)
		n.handleReady()
	})
	n.obs.inc(mSnapshotInstalls)
	n.obs.info("consensus: snapshot installed", lattr("index", meta.Position.Index), lattr("from", uint64(req.From.ID)))
	return nil
}

// ---- pre-admission JOIN (D4, R8-09) ----

func (n *Node) join(ctx context.Context, from coreconsensus.AuthenticatedPeer, r coreconsensus.JoinRequest) (coreconsensus.Position, errs.Code) {
	pos, code, _ := n.joinReason(ctx, from, r)
	return pos, code
}

// joinReason answers a JOIN and, when it refuses, says why: the
// authorizer's own words, sanitized and bounded, or the engine's rule. An
// authorizer that says nothing leaves the reason empty.
func (n *Node) joinReason(ctx context.Context, from coreconsensus.AuthenticatedPeer, r coreconsensus.JoinRequest) (coreconsensus.Position, errs.Code, string) {
	refuse := func(why string) (coreconsensus.Position, errs.Code, string) {
		return coreconsensus.Position{}, coreconsensus.CodeJoinRefused, why
	}
	switch {
	case n.cfg.JoinAuthorizer == nil:
		return refuse("this cluster admits no JOIN")
	case r.Cluster != n.ident.Cluster:
		return refuse("another cluster")
	case r.Slot != from.Slot:
		return refuse("the slot differs from the certificate's")
	case from.SPKI == ([32]byte{}):
		return refuse("no key")
	}
	r.SPKI = from.SPKI
	// One JOIN per slot per election timeout (R7-42).
	n.joinMu.Lock()
	if n.lastJoin == nil {
		n.lastJoin = map[string]time.Time{}
	}
	now := n.clk.Now()
	if last, ok := n.lastJoin[r.Slot]; ok && now.Sub(last) < n.Status().Timing.ElectionTimeoutMin {
		n.joinMu.Unlock()
		n.joinRefused.Add(1)
		n.obs.inc(mJoinsRateLimited)
		return refuse("one JOIN per slot per election timeout")
	}
	n.lastJoin[r.Slot] = now
	n.joinMu.Unlock()
	for _, m := range n.Membership().Members {
		if m.Slot == r.Slot {
			// A slot that still has a live member is refused (R8-11).
			return refuse("the slot still has a member")
		}
	}
	op, err := n.cfg.JoinAuthorizer.Authorize(ctx, r)
	if err != nil {
		n.obs.warn("consensus: JOIN refused by the authorizer", lstr("slot", r.Slot), lstr("error", err.Error()))
		return refuse(joinReasonOf(err))
	}
	if !op.Valid() {
		return refuse("the authorizer returned an invalid operator")
	}
	m := coreconsensus.Member{ID: r.ID, Incarnation: r.Incarnation, Slot: r.Slot, SPKI: r.SPKI, Address: r.Address, Joining: true}
	a := wire.Admin{
		Operator: string(op), Change: wire.ChangeAddLearner, Target: m.ID,
		Membership: coreconsensus.Membership{Members: []coreconsensus.Member{m}},
	}
	data := wire.AppendAdmin(nil, &a)
	tok, _, err := n.submit(ctx, coreconsensus.Entry{Kind: coreconsensus.EntryMembership, BodySum: wire.BodySum(data), Data: data}, true)
	if err != nil {
		c, ok := errs.CodeOf(err)
		if !ok {
			c = coreconsensus.CodeJoinRefused
		}
		reason, _ := errs.ReasonOf(err)
		return coreconsensus.Position{}, c, reason
	}
	return tok.Position(), 0, ""
}
