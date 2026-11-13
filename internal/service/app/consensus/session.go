package consensus

import (
	"context"
	"slices"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The client sessions of ADR 0241 (#918). A session command is an
// EntrySessionCommand whose payload is the session, the sequence and the
// command (wire.AppendSession). The apply goroutine folds each one into the
// node's table (raft.SessionTable) before the machine sees it: a command of
// a sequence the session has not applied is handed to the machine as an
// EntryCommand carrying the command alone, any other is answered from the
// table and never applied. The table rides every snapshot this node cuts
// (SnapshotMetaValue.Sessions), is restored with the machine, and is folded
// again from the log the start replays, so a retry after any restart,
// install or leader change finds it.

// OpenSession opens client session s for ProposeSession (ADR 0241). It is
// refused PAYLOAD_VERSION_REFUSED before the cluster version is 4, and
// opening an open session again is a no-op, so the call is retried
// blindly.
func (n *Node) OpenSession(ctx context.Context, s coreconsensus.SessionID) error {
	return n.proposeSession(ctx, s, 0, nil).err
}

// ProposeSession replicates data as command seq of the open session s and
// returns, once it is applied, its token and the value Apply returned:
// the state machine applies it at most once, whatever the retries. A
// retry after OUTCOME_UNKNOWN, with the same arguments, through any node,
// is answered with the first one's token and, while this node holds it,
// its value (ADR 0241). SESSION_DIGEST_MISMATCH and SESSION_EXPIRED apply
// nothing.
func (n *Node) ProposeSession(ctx context.Context, s coreconsensus.SessionID, seq uint64, data []byte) (coreconsensus.TokenValue, any, error) {
	if seq == 0 {
		// Sequence 0 is the opening's, never a command's.
		return coreconsensus.TokenValue{}, nil, origin(coreconsensus.SessionExpired, errs.String("why", "sequence 0 opens a session"))
	}
	r := n.proposeSession(ctx, s, seq, data)
	return r.tok, r.val, r.err
}

// proposeSession submits the session entry of command seq of s, the
// opening for seq 0.
func (n *Node) proposeSession(ctx context.Context, s coreconsensus.SessionID, seq uint64, data []byte) propResult {
	if len(data) > n.cfg.MaxEntryBytes {
		return propResult{err: origin(coreconsensus.EntryTooLarge, errs.Int("bytes", len(data)))}
	}
	// AppendSession copies data: the caller keeps its buffer (#508).
	payload := wire.AppendSession(make([]byte, 0, 24+len(data)), s, seq, data)
	e := coreconsensus.EntryValue{Kind: coreconsensus.EntrySessionCommand, BodySum: wire.BodySum(payload), Data: payload}
	return n.submitSession(ctx, e)
}

// submitSession is submit for a session command.
func (n *Node) submitSession(ctx context.Context, e coreconsensus.EntryValue) propResult {
	if err := ctx.Err(); err != nil {
		return propResult{err: err}
	}
	if n.stopped.Load() {
		return propResult{err: n.stoppedErr()}
	}
	p := &proposal{entry: e, done: make(chan propResult, 1)}
	n.pendingProp.Add(1)
	defer n.pendingProp.Add(-1)
	select {
	case n.propc <- p:
	case <-ctx.Done():
		return propResult{err: ctx.Err()}
	case <-n.stopc:
		return propResult{err: n.stoppedErr()}
	}
	r := n.awaitProposal(ctx, p)
	if r.err != nil {
		n.obs.inc(mProposalsFailed, codeAttr(r.err))
	}
	return r
}

// proposeSessionAt appends a session command at this leader, or answers it
// with the copy it already knows: one at or below the commit is applied,
// or about to be on this node, and is answered as applied once it is.
func (n *Node) proposeSessionAt(now time.Time, p *proposal) {
	pos, code := n.r.ProposeSession(now, p.entry)
	if code != 0 {
		p.done <- propResult{err: origin(sentinelOf(code), errs.String("leader", n.stamp.ID.String()))}
		return
	}
	n.appendedSession(p, pos, n.r.Status().Commit, true)
}

// appendedSession registers a session command at pos, which the leader
// answered with commit as its commit: a position past it is waited on as
// any proposal's; one at or below it is the committed copy of an earlier
// submission, whose waiter is gone, and is answered once this node applied
// it, with its value while the node holds it. That answer waits on a
// goroutine of its own, counted in n.wg as every goroutine the loop starts,
// so Close outlives it: it ends once the copy is applied or the loop is
// done. It runs on the loop.
func (n *Node) appendedSession(p *proposal, pos coreconsensus.PositionValue, commit uint64, local bool) {
	if pos.Index > commit {
		n.appended(p, pos, local)
		return
	}
	p.pos, p.local = pos, false
	p.stage.Store(stageAppended)
	n.wg.Add(1)
	go n.answerCommittedCopy(p, pos)
}

// answerCommittedCopy answers p once this node applied the committed copy
// at pos, off the loop: the apply may lag the commit. A node that stops
// first answers its stop.
func (n *Node) answerCommittedCopy(p *proposal, pos coreconsensus.PositionValue) {
	defer n.wg.Done()
	if !n.waitApplied(nil, pos.Index) {
		p.done <- propResult{err: outcomeUnknown("node stopped")}
		return
	}
	n.am.Lock()
	r, ok := n.keptResult(pos.Index)
	n.am.Unlock()
	var val any
	if ok && r.term == pos.Term {
		val = r.val
	}
	p.done <- propResult{tok: n.token(pos), val: val}
}

// applySession folds the committed session command e into the node's table
// and applies it, or answers it, as the table says: a command it has not
// seen is handed to the machine as the command it carries, once; an opening
// and a copy are answered and applied nowhere. It returns false when the
// machine failed and the node is stopping.
func (n *Node) applySession(e *coreconsensus.EntryValue) bool {
	answer, code := n.sessionTable().Apply(e)
	_, seq, data, err := wire.ParseSession(e.Data)
	if code != 0 || err != nil || answer != e.Position || seq == 0 {
		n.applyLag.Add(-int64(len(e.Data)))
		n.appliedAnswer(e, nil, answer, code)
		return true
	}
	c := *e
	c.Kind, c.Data = coreconsensus.EntryCommand, data
	c.Token = n.token(e.Position)
	results := make([]any, 1)
	applyStart := n.clk.Now()
	aerr := n.safeApply(nil, false, []coreconsensus.EntryValue{c}, results)
	n.obs.record(mApplyLatency, n.clk.Since(applyStart).Seconds())
	if aerr != nil {
		n.stop(aerr, false)
		return false
	}
	n.applyLag.Add(-int64(len(e.Data)))
	n.appliedAnswer(e, results[0], e.Position, 0)
	return true
}

// sessionsAt is the session table as the entries through applied left it:
// the snapshot's, then every session command the start's tail holds past it
// through the index a durable machine resumes from. The start loads every
// session command with its payload (readTail drops only a command's).
func sessionsAt(base *coreconsensus.SnapshotMetaValue, ents []coreconsensus.EntryValue, applied uint64) *raft.SessionTable {
	t := raft.NewSessionTable(slices.Clone(base.Sessions))
	for i := range ents {
		e := &ents[i]
		if e.Position.Index <= base.Position.Index || e.Position.Index > applied {
			continue
		}
		if e.Kind == coreconsensus.EntrySessionCommand && e.Data != nil {
			t.Apply(e)
		}
	}
	return t
}

// sessionTable is the node's session table, an empty one before a start
// set it.
func (n *Node) sessionTable() *raft.SessionTable {
	if n.sessions == nil {
		n.sessions = raft.NewSessionTable(nil)
	}
	return n.sessions
}
