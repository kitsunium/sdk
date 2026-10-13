package consensus

import (
	"context"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// Propose replicates data and returns, once the entry is applied locally,
// its token and the value Apply returned. NOT_LEADER means certainly not
// appended; OUTCOME_UNKNOWN means it may still commit: read the state through
// Barrier before deciding, never retry blindly (D6, R1-04).
func (n *Node) Propose(ctx context.Context, data []byte) (coreconsensus.Token, any, error) {
	if len(data) > n.cfg.MaxEntryBytes {
		return coreconsensus.Token{}, nil, origin(coreconsensus.EntryTooLarge, errs.Int("bytes", len(data)))
	}
	e := coreconsensus.Entry{Kind: coreconsensus.EntryCommand, BodySum: wire.BodySum(data), Data: data}
	return n.submit(ctx, e, false)
}

func (n *Node) submit(ctx context.Context, e coreconsensus.Entry, admin bool) (coreconsensus.Token, any, error) {
	if err := ctx.Err(); err != nil {
		return coreconsensus.Token{}, nil, err
	}
	if n.stopped.Load() {
		return coreconsensus.Token{}, nil, n.stoppedErr()
	}
	p := &proposal{entry: e, admin: admin, done: make(chan propResult, 1)}
	select {
	case n.propc <- p:
	case <-ctx.Done():
		return coreconsensus.Token{}, nil, ctx.Err()
	case <-n.stopc:
		return coreconsensus.Token{}, nil, n.stoppedErr()
	}
	select {
	case r := <-p.done:
		return r.tok, r.val, r.err
	case <-ctx.Done():
		if p.stage.Load() == stageUnsent {
			// Ended before the append: ctx.Err(), as everywhere in the SDK.
			return coreconsensus.Token{}, nil, ctx.Err()
		}
		return coreconsensus.Token{}, nil, origin(coreconsensus.OutcomeUnknown,
			errs.String("why", "context ended after the append"), errs.String("cause", ctx.Err().Error()))
	}
}

// handleProposals appends every proposal waiting at this turn as one batch
// at a leader, or forwards each to the leader (D6).
func (n *Node) handleProposals(ps []*proposal) {
	st := n.r.Status()
	now := n.clk.Now()
	if st.Role != coreconsensus.RoleLeader {
		for _, p := range ps {
			n.forward(p, st)
		}
		return
	}
	var batch []*proposal
	var ents []coreconsensus.Entry
	flush := func() {
		if len(batch) == 0 {
			return
		}
		pos, code := n.r.Propose(now, ents)
		for i, p := range batch {
			if code != 0 {
				p.done <- propResult{err: origin(sentinelOf(code), errs.String("leader", st.Lead.String()))}
				continue
			}
			n.appended(p, pos[i], true)
		}
		batch, ents = batch[:0], ents[:0]
	}
	for _, p := range ps {
		if !p.admin && n.pendingBytes.Load()+int64(len(p.entry.Data)) > n.cfg.MaxPendingBytes {
			// Refused before anything is appended (D6, from IWFS).
			p.done <- propResult{err: origin(coreconsensus.Overloaded)}
			continue
		}
		if p.admin {
			flush()
			pos, code := n.r.Propose(now, []coreconsensus.Entry{p.entry})
			if code != 0 {
				p.done <- propResult{err: origin(sentinelOf(code))}
				continue
			}
			n.appended(p, pos[0], true)
			continue
		}
		batch = append(batch, p)
		ents = append(ents, p.entry)
	}
	flush()
}

// appended registers a proposal's waiter at its position.
func (n *Node) appended(p *proposal, pos coreconsensus.Position, local bool) {
	p.pos, p.local = pos, local
	p.stage.Store(stageAppended)
	if local {
		n.pendingBytes.Add(int64(len(p.entry.Data)))
	}
	n.am.Lock()
	if pos.Index <= n.applied {
		r, ok := n.results[pos.Index]
		n.am.Unlock()
		switch {
		case ok && r.term == pos.Term:
			p.done <- propResult{tok: n.token(pos), val: r.val}
		case ok:
			p.done <- propResult{err: withToken(coreconsensus.EntrySuperseded, n.token(pos))}
		default:
			p.done <- propResult{err: outcomeUnknown("applied before its position was known")}
		}
		return
	}
	n.waiters[pos.Index] = p
	n.am.Unlock()
}

// forward sends a proposal to the leader under a submission id that never
// repeats across restarts: a random epoch in the high 32 bits, a counter in
// the low 32, and a fresh epoch before the counter runs out (R7-23, R9-17).
func (n *Node) forward(p *proposal, st raft.Status) {
	if st.Lead == 0 || st.Lead == n.stamp.ID {
		p.done <- propResult{err: notLeader(0)}
		return
	}
	if n.subCount == ^uint32(0) {
		n.subEpoch, n.subCount = newSubEpoch(), 0
	}
	n.subCount++
	ctx := n.subEpoch | uint64(n.subCount)
	kind := coreconsensus.MsgForward
	if p.admin {
		kind = coreconsensus.MsgForwardAdmin
	}
	m := coreconsensus.Message{Version: 1, Group: n.cfg.Group, Kind: kind, From: n.stamp.ID, To: st.Lead,
		Term: st.Term, Context: ctx, Entries: []coreconsensus.Entry{p.entry}}
	n.forwards[ctx] = p
	n.am.Lock()
	n.keepResults++
	n.am.Unlock()
	p.stage.Store(stagePossible)
	n.cfg.Transport.Send(st.Lead, wire.AppendMessage(nil, &m))
}

// forwardAnswered resolves a forwarded proposal from the leader's answer.
func (n *Node) forwardAnswered(m coreconsensus.Message) {
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
	n.appended(p, coreconsensus.Position{Term: m.LogTerm, Index: m.Index}, false)
}

// ProposeAs types the Apply result. A result of another dynamic type is
// RESULT_TYPE_MISMATCH carrying the committed token: the entry IS committed.
// A nil result is the zero R.
func ProposeAs[R any](ctx context.Context, r coreconsensus.Replicated, data []byte) (coreconsensus.Token, R, error) {
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
func (n *Node) Barrier(ctx context.Context) (coreconsensus.Token, error) {
	if n.stopped.Load() {
		return coreconsensus.Token{}, n.stoppedErr()
	}
	w := &readWait{done: make(chan readResult, 1)}
	noLease := n.cfg.Reads != ReadLease
	if !n.exec(func() {
		n.readSeq++
		ctx := n.readSeq
		n.reads[ctx] = w
		n.r.ReadIndex(n.clk.Now(), ctx, noLease)
		n.handleReady()
	}) {
		return coreconsensus.Token{}, n.stoppedErr()
	}
	var res readResult
	select {
	case res = <-w.done:
	case <-ctx.Done():
		return coreconsensus.Token{}, origin(coreconsensus.LeadershipUnconfirmed, errs.String("cause", ctx.Err().Error()))
	}
	switch res.code {
	case 0:
	case coreconsensus.CodeNodeStopped:
		return coreconsensus.Token{}, n.stoppedErr()
	default:
		return coreconsensus.Token{}, origin(sentinelOf(res.code), errs.String("leader", n.Status().Leader.String()))
	}
	if !n.waitApplied(ctx.Done(), res.index) {
		if ctx.Err() != nil {
			return coreconsensus.Token{}, ctx.Err()
		}
		return coreconsensus.Token{}, n.stoppedErr()
	}
	n.am.Lock()
	tok := n.token(coreconsensus.Position{Term: n.appliedTerm, Index: n.applied})
	n.am.Unlock()
	return tok, nil
}

// WaitApplied returns once the local node has applied t. A token of another
// lineage is LINEAGE_MISMATCH.
func (n *Node) WaitApplied(ctx context.Context, t coreconsensus.Token) error {
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
	return nil
}

// ---- administration (D17): every operation takes its operator, can be
// called on any node and writes an entry ----

func (n *Node) admin(ctx context.Context, kind coreconsensus.EntryKind, a wire.Admin) error {
	if !coreconsensus.Operator(a.Operator).Valid() {
		return origin(coreconsensus.MembershipInvalid, errs.String("why", "operator"))
	}
	data := wire.AppendAdmin(nil, &a)
	_, _, err := n.submit(ctx, coreconsensus.Entry{Kind: kind, BodySum: wire.BodySum(data), Data: data}, true)
	return err
}

// AddLearner adds a non-voting member that receives the log (D8).
func (n *Node) AddLearner(ctx context.Context, op coreconsensus.Operator, m coreconsensus.Member) error {
	return n.admin(ctx, coreconsensus.EntryMembership, wire.Admin{Operator: string(op), Change: wire.ChangeAddLearner,
		Target: m.ID, Membership: coreconsensus.Membership{Members: []coreconsensus.Member{m}}})
}

// Promote makes a caught-up learner a voter (D8).
func (n *Node) Promote(ctx context.Context, op coreconsensus.Operator, id coreconsensus.NodeID) error {
	return n.admin(ctx, coreconsensus.EntryMembership, wire.Admin{Operator: string(op), Change: wire.ChangePromote, Target: id})
}

// Remove removes a member and retires its id; removing the leader first
// transfers leadership (D8).
func (n *Node) Remove(ctx context.Context, op coreconsensus.Operator, id coreconsensus.NodeID) error {
	if st := n.Status(); st.Leader == id && id == n.stamp.ID {
		n.handOff(ctx)
	}
	return n.admin(ctx, coreconsensus.EntryMembership, wire.Admin{Operator: string(op), Change: wire.ChangeRemove, Target: id})
}

// UpdateAddress changes where a member listens.
func (n *Node) UpdateAddress(ctx context.Context, op coreconsensus.Operator, id coreconsensus.NodeID, addr string) error {
	return n.admin(ctx, coreconsensus.EntryMembership, wire.Admin{Operator: string(op), Change: wire.ChangeUpdateAddress,
		Target: id, Membership: coreconsensus.Membership{Members: []coreconsensus.Member{{ID: id, Address: addr}}}})
}

// DenyPin withdraws a member's SPKI pin (R9-18).
func (n *Node) DenyPin(ctx context.Context, op coreconsensus.Operator, id coreconsensus.NodeID) error {
	return n.admin(ctx, coreconsensus.EntryMembership, wire.Admin{Operator: string(op), Change: wire.ChangeDenyPin, Target: id})
}

// ProposeTimingContract proposes a new timing contract.
func (n *Node) ProposeTimingContract(ctx context.Context, op coreconsensus.Operator, pre coreconsensus.Precondition, c coreconsensus.TimingContract) error {
	return n.admin(ctx, coreconsensus.EntryTimingContract, wire.Admin{Operator: string(op), Pre: pre, Contract: c})
}

// RaiseClusterVersion raises the protocol version every member must speak.
func (n *Node) RaiseClusterVersion(ctx context.Context, op coreconsensus.Operator, pre coreconsensus.Precondition, v uint32) error {
	return n.admin(ctx, coreconsensus.EntryClusterVersion, wire.Admin{Operator: string(op), Pre: pre, ClusterVersion: v})
}

// ClearNoSpace clears the no-space alarm the precondition names.
func (n *Node) ClearNoSpace(ctx context.Context, op coreconsensus.Operator, pre coreconsensus.Precondition) error {
	return n.admin(ctx, coreconsensus.EntryNoSpaceClear, wire.Admin{Operator: string(op), Pre: pre})
}

// TransferLeadership moves leadership to a voter (D8). It records the
// operator in an entry, then waits until the target leads or one election
// timeout passes.
func (n *Node) TransferLeadership(ctx context.Context, op coreconsensus.Operator, to coreconsensus.NodeID) error {
	err := n.admin(ctx, coreconsensus.EntryTransfer, wire.Admin{Operator: string(op), Target: to})
	switch {
	case err == nil, errs.HasCode(err, coreconsensus.CodeOutcomeUnknown):
		// The transfer entry may have committed and the transfer may be
		// under way: its outcome is the leader, not the entry's answer.
		return n.awaitLeader(ctx, to)
	case n.Status().Leader == to:
		return nil
	}
	return err
}

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
