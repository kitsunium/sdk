package consensus

import (
	"context"
	"slices"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// Propose replicates data and returns, once the entry is applied locally,
// its token and the value Apply returned. NOT_LEADER means certainly not
// appended; OUTCOME_UNKNOWN means it may still commit: read the state through
// Barrier before deciding, never retry blindly (D6, R1-04).
func (n *Node) Propose(ctx context.Context, data []byte) (coreconsensus.TokenValue, any, error) {
	if len(data) > n.cfg.MaxEntryBytes {
		return coreconsensus.TokenValue{}, nil, origin(coreconsensus.EntryTooLarge, errs.Int("bytes", len(data)))
	}
	// The entry owns a copy: the leader keeps it in memory and sends it for
	// as long as a peer lags, and a caller that reused its buffer after the
	// call changed it under its body sum, which every follower then refused
	// (#508).
	data = slices.Clone(data)
	e := coreconsensus.EntryValue{Kind: coreconsensus.EntryCommand, BodySum: wire.BodySum(data), Data: data}
	return n.submit(ctx, e, false)
}

// submit hands an entry to the event loop and waits for its outcome. Before
// the loop has it, a cancelled ctx or a stop answers with ctx.Err() or the
// stop's reason; after it is appended, a cancelled ctx is OUTCOME_UNKNOWN,
// because the entry may still commit. An admin entry bypasses the pending
// and apply-lag budgets, so an operator can always reach a saturated node.
func (n *Node) submit(ctx context.Context, e coreconsensus.EntryValue, admin bool) (coreconsensus.TokenValue, any, error) {
	if err := ctx.Err(); err != nil {
		return coreconsensus.TokenValue{}, nil, err
	}
	if n.stopped.Load() {
		return coreconsensus.TokenValue{}, nil, n.stoppedErr()
	}
	p := &proposal{entry: e, admin: admin, done: make(chan propResult, 1)}
	start := n.clk.Now()
	span := n.obs.span(ctx, "consensus.Propose")
	n.pendingProp.Add(1)
	n.obs.setGauge(mProposalsPending, float64(n.pendingProp.Load()))
	defer func() {
		n.pendingProp.Add(-1)
		n.obs.setGauge(mProposalsPending, float64(n.pendingProp.Load()))
	}()
	finish := func(r propResult) (coreconsensus.TokenValue, any, error) {
		if r.err != nil {
			n.obs.inc(mProposalsFailed, codeAttr(r.err))
		} else {
			n.obs.record(mCommitLatency, n.clk.Since(start).Seconds())
			spanEvent(span, "applied", coreotel.Int64("index", int64(r.tok.Index)))
		}
		spanEnd(span, r.err)
		return r.tok, r.val, r.err
	}
	select {
	case n.propc <- p:
	case <-ctx.Done():
		return finish(propResult{err: ctx.Err()})
	case <-n.stopc:
		return finish(propResult{err: n.stoppedErr()})
	}
	return finish(n.awaitProposal(ctx, p))
}

// awaitProposal waits for the loop's answer to a proposal it already holds.
// A ctx that ends first is ctx.Err() only while the proposal is provably
// unsent; once it may have been appended the answer is OUTCOME_UNKNOWN.
//
// The loop may also end without ever seeing it: a submit that passed the
// stopped check finds both propc and stopc ready, and Go picks one at
// random, after failAllOnStop drained propc for the last time. Once the
// loop is gone an answer it posted wins; otherwise the proposal never left
// and is NODE_STOPPED, or OUTCOME_UNKNOWN if it may have been appended
// (#420).
func (n *Node) awaitProposal(ctx context.Context, p *proposal) propResult {
	select {
	case r := <-p.done:
		return r
	case <-n.loopDone:
		select {
		case r := <-p.done:
			return r
		default:
		}
		if p.stage.Load() == stageUnsent {
			return propResult{err: n.stoppedErr()}
		}
		return propResult{err: outcomeUnknown("node stopped")}
	case <-ctx.Done():
		if p.stage.CompareAndSwap(stageUnsent, stageAbandoned) {
			// Ended before the loop took it: the loop drops it, nothing is
			// appended, and the answer is ctx.Err(), as everywhere in the
			// SDK. Read without the swap, the stage said unsent while the
			// proposal sat in propc, and the loop appended it afterwards: a
			// caller told ctx.Err() retried a command that was applied
			// (#519).
			return propResult{err: ctx.Err()}
		}
		// A forward the leader never answers is dropped at the next
		// sweep rather than kept until the next leader change (#590).
		p.gone.Store(true)
		return propResult{err: origin(coreconsensus.OutcomeUnknown,
			errs.String("why", "context ended after the append"), errs.String("cause", ctx.Err().Error()))}
	}
}

// handleProposals appends every proposal waiting at this turn as one batch
// at a leader, or forwards each to the leader (D6).
func (n *Node) handleProposals(ps []*proposal) {
	ps = claimed(ps)
	st := n.r.Status()
	now := n.clk.Now()
	if st.Role != coreconsensus.RoleLeader {
		for _, p := range ps {
			n.forward(p, st)
		}
		return
	}
	var batch []*proposal
	var ents []coreconsensus.EntryValue
	flush := func() {
		n.proposeBatch(now, st.Lead, batch, ents)
		batch, ents = batch[:0], ents[:0]
	}
	for _, p := range ps {
		if n.refuseOverload(p) || n.refuseShort(p) {
			continue
		}
		if p.admin {
			flush()
			n.proposeAlone(now, p)
			continue
		}
		batch = append(batch, p)
		ents = append(ents, p.entry)
	}
	flush()
}

// claimed keeps, in place, the proposals the loop takes from their callers:
// each moves from unsent to possibly accepted, before raft or the leader
// sees it, unless its caller abandoned it first, and an abandoned proposal
// is dropped unanswered, its caller already told ctx.Err() (#519).
func claimed(ps []*proposal) []*proposal {
	k := 0
	for _, p := range ps {
		if p.stage.CompareAndSwap(stageUnsent, stagePossible) {
			ps[k] = p
			k++
		}
	}
	return ps[:k]
}

// proposeBatch appends a batch of ordinary proposals as one Propose and
// registers each at its position, or answers every one with raft's refusal
// when the batch as a whole is refused; an empty batch does nothing.
func (n *Node) proposeBatch(now time.Time, lead coreconsensus.NodeID, batch []*proposal, ents []coreconsensus.EntryValue) {
	if len(batch) == 0 {
		return
	}
	pos, code := n.r.Propose(now, ents)
	for i, p := range batch {
		if code != 0 {
			p.done <- propResult{err: origin(sentinelOf(code), errs.String("leader", lead.String()))}
			continue
		}
		n.appended(p, pos[i], true)
	}
}

// refuseOverload answers an ordinary proposal with OVERLOADED, before
// anything is appended, when the pending bytes or the apply lag are over
// budget, and reports whether it did. An admin proposal is never refused
// here, so the operator can still act on an overloaded node.
func (n *Node) refuseOverload(p *proposal) bool {
	if p.admin {
		return false
	}
	if n.pendingBytes.Load()+int64(len(p.entry.Data)) > n.cfg.MaxPendingBytes {
		// Refused before anything is appended (D6, from IWFS).
		p.done <- propResult{err: origin(coreconsensus.Overloaded, errs.String("why", "pending bytes"))}
		return true
	}
	if n.applyOverloaded() {
		// Apply backpressure: no new proposal until apply catches up
		// (D6, R1-14).
		p.done <- propResult{err: origin(coreconsensus.Overloaded, errs.String("why", "apply lag"))}
		return true
	}
	return false
}

// proposeAlone appends one proposal as its own entry, outside any batch, as
// an administrative entry must be, and answers it at once on a refusal.
func (n *Node) proposeAlone(now time.Time, p *proposal) {
	pos, code := n.r.Propose(now, []coreconsensus.EntryValue{p.entry})
	if code != 0 {
		p.done <- propResult{err: origin(sentinelOf(code))}
		return
	}
	n.appended(p, pos[0], true)
}

// appended registers a proposal's waiter at its position.
func (n *Node) appended(p *proposal, pos coreconsensus.PositionValue, local bool) {
	p.pos, p.local = pos, local
	p.stage.Store(stageAppended)
	if local {
		n.pendingBytes.Add(int64(len(p.entry.Data)))
	}
	n.am.Lock()
	if pos.Index <= n.applied {
		r, ok := n.results[pos.Index]
		n.am.Unlock()
		n.answerApplied(p, pos, r, ok)
		return
	}
	displaced := p
	if old := n.waiters[pos.Index]; old == nil || old.pos.Term < pos.Term {
		n.waiters[pos.Index], displaced = p, old
	}
	n.am.Unlock()
	if displaced != nil {
		n.displace(displaced)
	}
}

// answerApplied answers a proposal whose position is already applied,
// from the result kept there when ok: it is never registered, so no applied1
// releases its pending bytes (#419).
func (n *Node) answerApplied(p *proposal, pos coreconsensus.PositionValue, r applyResult, ok bool) {
	if p.local {
		n.pendingBytes.Add(-int64(len(p.entry.Data)))
	}
	switch {
	case ok && r.term == pos.Term:
		p.done <- propResult{tok: n.token(pos), val: r.val}
	case ok:
		p.done <- propResult{err: withToken(coreconsensus.EntrySuperseded, n.token(pos))}
	default:
		p.done <- propResult{err: outcomeUnknown("applied before its position was known")}
	}
}

// displace answers a proposal that lost its index's waiter to another one
// appended at the same index under a later term: after a leader change, the
// new leader hands out first the indexes the old one's unreplicated tail
// held. Overwritten, the earlier waiter was answered by nothing — applied1,
// sweepWaiters, answerCovered and failAllOnStop all walk the map — and its
// caller waited for its own deadline, forever with none, its pending bytes
// counted for good (#517). Either entry may still commit, so the answer is
// OUTCOME_UNKNOWN, never ENTRY_SUPERSEDED (D6).
func (n *Node) displace(p *proposal) {
	if p.local {
		n.pendingBytes.Add(-int64(len(p.entry.Data)))
	}
	p.done <- propResult{err: outcomeUnknown("another proposal took its index under a later term")}
}

// forward sends a proposal to the leader under a submission id that never
// repeats across restarts (nextID).
func (n *Node) forward(p *proposal, st raft.StatusValue) {
	if st.Lead == 0 || st.Lead == n.stamp.ID {
		p.done <- propResult{err: notLeader(0)}
		return
	}
	ctx := n.nextID()
	kind := coreconsensus.MsgForward
	if p.admin {
		kind = coreconsensus.MsgForwardAdmin
	}
	m := coreconsensus.MessageValue{
		Version: 1, Group: n.cfg.Group, Kind: kind, From: n.stamp.ID, To: st.Lead,
		Term: st.Term, Context: ctx, Entries: []coreconsensus.EntryValue{p.entry},
	}
	p.sentAt = n.clk.Now()
	n.forwards[ctx] = p
	n.am.Lock()
	n.keepResults++
	n.am.Unlock()
	p.stage.Store(stagePossible)
	n.cfg.Transport.Send(st.Lead, wire.AppendMessage(nil, &m))
}

// nextID is the next submission id of a forward or a read: a random epoch
// drawn at start in the high 32 bits, a counter in the low 32, and a fresh
// epoch before the counter runs out, so an id never repeats across restarts
// (R7-23, R9-17). A leader may still hold a read of this node's previous
// incarnation, unconfirmed, and answer it later: under a counter that
// restarted at 1, the new incarnation's read of the same number took that
// answer, at the old commit index, below a write acknowledged before the
// call (#510).
func (n *Node) nextID() uint64 {
	if n.subCount == ^uint32(0) {
		n.subEpoch, n.subCount = newSubEpoch(), 0
	}
	n.subCount++
	return n.subEpoch | uint64(n.subCount)
}

// forwardAnswered resolves a forwarded proposal from the leader's answer.
func (n *Node) forwardAnswered(m coreconsensus.MessageValue) {
	p, ok := n.forwards[m.Context]
	if !ok {
		return
	}
	n.dropForward(m.Context)
	if m.Reject {
		// A rejection that correlates with the submission and says nothing
		// was appended is the one exception to OUTCOME_UNKNOWN (D6).
		p.done <- propResult{err: origin(sentinelOf(m.Code), errs.String("leader", coreconsensus.NodeID(m.RejectHint).String()))}
		return
	}
	n.appended(p, coreconsensus.PositionValue{Term: m.LogTerm, Index: m.Index}, false)
}

// ProposeAs types the Apply result. A result of another dynamic type is
// RESULT_TYPE_MISMATCH carrying the committed token: the entry IS committed.
// A nil result is the zero R.
func ProposeAs[R any](ctx context.Context, r coreconsensus.Replicated, data []byte) (coreconsensus.TokenValue, R, error) {
	var zero R
	tok, v, err := r.Propose(ctx, data)
	if err != nil {
		return tok, zero, err
	}
	if v == nil {
		return tok, zero, nil
	}
	out, ok := v.(R)
	if !ok {
		return tok, zero, withToken(coreconsensus.ResultTypeMismatch, tok)
	}
	return tok, out, nil
}

// Barrier returns once the local state machine reflects every write
// committed before the call (D9).
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
	if !n.exec(func() {
		ctx := n.nextID()
		w.sentAt = n.clk.Now()
		w.askedAt = w.sentAt
		n.reads[ctx] = w
		n.r.ReadIndex(w.sentAt, ctx, noLease)
		n.handleReady()
	}) {
		return coreconsensus.TokenValue{}, n.stoppedErr()
	}
	var res readResult
	select {
	case res = <-w.done:
	case <-ctx.Done():
		w.gone.Store(true)
		return coreconsensus.TokenValue{}, origin(coreconsensus.LeadershipUnconfirmed, errs.String("cause", ctx.Err().Error()))
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

// ---- administration (D17): every operation takes its operator, can be
// called on any node and writes an entry ----

// admin validates the operator, then submits an administrative entry and
// logs it at Warn with its outcome whatever that outcome is, so every
// operator action leaves a trace (D8, D17).
func (n *Node) admin(ctx context.Context, kind coreconsensus.EntryKind, a wire.AdminValue) error {
	if !coreconsensus.Operator(a.Operator).Valid() {
		return origin(coreconsensus.MembershipInvalid, errs.String("why", "operator"))
	}
	data := wire.AppendAdmin(nil, &a)
	_, _, err := n.submit(ctx, coreconsensus.EntryValue{Kind: kind, BodySum: wire.BodySum(data), Data: data}, true)
	// Every administrative operation is logged at Warn with its operator
	// (D8, R3-14).
	outcome := "ok"
	if err != nil {
		outcome = err.Error()
	}
	n.obs.warn("consensus: administrative operation", lstr("operator", a.Operator), lattr("kind", uint64(kind)),
		lattr("change", uint64(a.Change)), lattr("target", uint64(a.Target)), lstr("outcome", outcome))
	return err
}

// AddLearner adds a non-voting member that receives the log (D8).
func (n *Node) AddLearner(ctx context.Context, op coreconsensus.Operator, m coreconsensus.MemberValue) error {
	return n.admin(ctx, coreconsensus.EntryMembership, wire.AdminValue{
		Operator: string(op), Change: wire.ChangeAddLearner,
		Target: m.ID, Membership: coreconsensus.MembershipValue{Members: []coreconsensus.MemberValue{m}},
	})
}

// Promote makes a caught-up learner a voter (D8).
func (n *Node) Promote(ctx context.Context, op coreconsensus.Operator, id coreconsensus.NodeID) error {
	// A learner that announced a snapshot limit below this node's newest
	// snapshot is refused MEMBERSHIP_INVALID: a voter's limit bounds every
	// cut, and every cut would fail SNAPSHOT_TOO_LARGE until it is removed
	// (#591).
	if m, ok := n.Membership().Member(id); ok && m.MaxSnapshotBytes > 0 {
		n.snapState.mu.Lock()
		newest := n.snapState.newestBytes
		n.snapState.mu.Unlock()
		if newest > m.MaxSnapshotBytes {
			return origin(coreconsensus.MembershipInvalid, errs.String("why", "the learner's snapshot limit is below the newest snapshot"),
				errs.Int64("limit", m.MaxSnapshotBytes), errs.Int64("bytes", newest))
		}
	}
	return n.admin(ctx, coreconsensus.EntryMembership, wire.AdminValue{Operator: string(op), Change: wire.ChangePromote, Target: id})
}

// Remove removes a member and retires its id; removing the leader first
// transfers leadership (D8).
func (n *Node) Remove(ctx context.Context, op coreconsensus.Operator, id coreconsensus.NodeID) error {
	remove := wire.AdminValue{Operator: string(op), Change: wire.ChangeRemove, Target: id}
	st := n.Status()
	switch {
	case st.Leader != id:
		return n.admin(ctx, coreconsensus.EntryMembership, remove)
	case id == n.stamp.ID:
		n.handOff(ctx)
	default:
		// The leader is another node: it refuses a removal of itself, so
		// the call moved leadership away first only when it landed on the
		// leader — on a follower it was refused MEMBERSHIP_INVALID (#529).
		// The transfer is an operator's act, recorded as one.
		to, ok := n.successorOf(id)
		if !ok {
			return origin(coreconsensus.MembershipInvalid, errs.String("why", "no other voter to lead"))
		}
		if err := n.TransferLeadership(ctx, op, to); err != nil {
			return err
		}
	}
	return n.removeAfterTransfer(ctx, remove)
}

// successorOf is the voter a removal of leader id hands leadership to: this
// node when it votes, else the first other voter of the configuration.
func (n *Node) successorOf(id coreconsensus.NodeID) (coreconsensus.NodeID, bool) {
	conf := n.Membership()
	if m, ok := conf.Member(n.stamp.ID); ok && m.Voter {
		return n.stamp.ID, true
	}
	for _, m := range conf.Members {
		if m.Voter && m.ID != id {
			return m.ID, true
		}
	}
	return 0, false
}

// removeAfterTransfer submits a removal right after leadership moved. The
// new leader accepts no membership change before its term's no-op commits
// (MEMBERSHIP_CHANGE_PENDING), an engine detail of the transfer the call
// itself made, not the caller's: it is submitted again, each heartbeat, for
// at most twice the maximum election timeout.
func (n *Node) removeAfterTransfer(ctx context.Context, remove wire.AdminValue) error {
	timing := n.Status().Timing
	deadline := n.clk.Now().Add(2 * timing.ElectionTimeoutMax)
	for {
		err := n.admin(ctx, coreconsensus.EntryMembership, remove)
		if !errs.HasCode(err, coreconsensus.CodeMembershipChangePending) || !n.clk.Now().Before(deadline) {
			return err
		}
		if !n.pause(ctx, timing.HeartbeatInterval) {
			return err
		}
	}
}

// pause waits d on the node's clock and reports whether it did, false
// when ctx ended first.
func (n *Node) pause(ctx context.Context, d time.Duration) bool {
	t := n.clk.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return true
	case <-ctx.Done():
		return false
	}
}

// UpdateAddress changes where a member listens.
func (n *Node) UpdateAddress(ctx context.Context, op coreconsensus.Operator, id coreconsensus.NodeID, addr string) error {
	members := []coreconsensus.MemberValue{{ID: id, Address: addr}}
	return n.admin(ctx, coreconsensus.EntryMembership, wire.AdminValue{
		Operator: string(op), Change: wire.ChangeUpdateAddress,
		Target: id, Membership: coreconsensus.MembershipValue{Members: members},
	})
}

// DenyPin withdraws a member's SPKI pin (R9-18). The transport refuses the
// member once the change commits. It is refused MEMBERSHIP_INVALID for the
// leader itself, and while the voters that answer the leader, the target
// left out, are not a majority (R2-27).
func (n *Node) DenyPin(ctx context.Context, op coreconsensus.Operator, id coreconsensus.NodeID) error {
	return n.admin(ctx, coreconsensus.EntryMembership, wire.AdminValue{Operator: string(op), Change: wire.ChangeDenyPin, Target: id})
}

// ProposeTimingContract proposes a new timing contract.
func (n *Node) ProposeTimingContract(ctx context.Context, op coreconsensus.Operator, pre coreconsensus.PreconditionSpec, c coreconsensus.TimingContractSpec) error {
	return n.admin(ctx, coreconsensus.EntryTimingContract, wire.AdminValue{Operator: string(op), Pre: pre, Contract: c})
}

// RaiseClusterVersion raises the protocol version every member must speak.
func (n *Node) RaiseClusterVersion(ctx context.Context, op coreconsensus.Operator, pre coreconsensus.PreconditionSpec, v uint32) error {
	return n.admin(ctx, coreconsensus.EntryClusterVersion, wire.AdminValue{Operator: string(op), Pre: pre, ClusterVersion: v})
}

// ClearNoSpace clears the no-space alarm the precondition names.
func (n *Node) ClearNoSpace(ctx context.Context, op coreconsensus.Operator, pre coreconsensus.PreconditionSpec) error {
	return n.admin(ctx, coreconsensus.EntryNoSpaceClear, wire.AdminValue{Operator: string(op), Pre: pre})
}

// TransferLeadership moves leadership to a voter (D8). It records the
// operator in an entry, then waits until the target leads or twice the
// maximum election timeout passes.
func (n *Node) TransferLeadership(ctx context.Context, op coreconsensus.Operator, to coreconsensus.NodeID) error {
	err := n.admin(ctx, coreconsensus.EntryTransfer, wire.AdminValue{Operator: string(op), Target: to})
	switch {
	case err == nil, errs.HasCode(err, coreconsensus.CodeOutcomeUnknown):
		// The transfer entry may have committed and the transfer may be
		// under way: its outcome is the leader, not the entry's answer.
		return n.awaitLeader(ctx, to)
	case n.Status().Leader == to:
		return n.confirmLeader(ctx, to, err)
	}
	return err
}

// confirmLeader turns the node's own view that to already leads into a
// fact before a refused transfer is reported as done. A view is the node's
// last turn, not the cluster: a leader deposed by a transfer it has not yet
// heard about still names itself, refuses a transfer to itself as
// MEMBERSHIP_INVALID, and would answer success while another node leads a
// newer term. A barrier is a read index the leader confirms with a quorum,
// so the view it leaves behind is current. A barrier that fails is
// TRANSFER_FAILED, the answer a transfer whose target does not lead owes
// (D8); a confirmed view naming another leader keeps the refusal.
func (n *Node) confirmLeader(ctx context.Context, to coreconsensus.NodeID, refused error) error {
	if _, err := n.Barrier(ctx); err != nil {
		return origin(coreconsensus.TransferFailed, errs.String("target", to.String()), errs.String("cause", err.Error()))
	}
	if n.Status().Leader == to {
		return nil
	}
	return refused
}

// transferLocal starts a leadership transfer on this node's own raft
// instance, without writing an entry, and waits for the target to lead: the
// hand-off Close and a leader's self-Remove use, where no operator entry is
// owed and the node may be about to leave.
func (n *Node) transferLocal(ctx context.Context, to coreconsensus.NodeID) error {
	var code errs.Code
	if !n.exec(func() {
		code = n.r.TransferLeadership(n.clk.Now(), to)
		n.handleReady()
	}) {
		return n.stoppedErr()
	}
	if code != 0 {
		return origin(sentinelOf(code))
	}
	return n.awaitLeader(ctx, to)
}

// awaitLeader waits until to is the leader the node observes. It fails with
// TRANSFER_FAILED after twice the maximum election timeout, or when ctx
// ends, and with the stop's reason when the node stops first.
func (n *Node) awaitLeader(ctx context.Context, to coreconsensus.NodeID) error {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := n.Watch(wctx)
	t := n.clk.NewTimer(2 * n.Status().Timing.ElectionTimeoutMax)
	defer t.Stop()
	for {
		select {
		case st, ok := <-ch:
			if !ok {
				return n.stoppedErr()
			}
			if st.Leader == to {
				return nil
			}
		case <-t.C():
			return origin(coreconsensus.TransferFailed, errs.String("target", to.String()))
		case <-ctx.Done():
			return origin(coreconsensus.TransferFailed, errs.String("cause", ctx.Err().Error()))
		}
	}
}
