package raft

import (
	"slices"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The refusals the step function returns, short names for the domain's codes:
// it answers with a code rather than an error, so a refusal costs no
// allocation on the event loop.
const (
	// maxFwdAnswers bounds the answers a node keeps of the forwards it
	// received. A duplicate is a frame a transport resent with
	// its batch on a fresh connection, right behind its original (#377,
	// #401): the newest answers are the ones kept, the oldest go first.
	maxFwdAnswers int = 4096
	// codeNotLeader refuses what only the leader of this term may do.
	codeNotLeader errs.Code = core.CodeNotLeader
	// codeChangePending refuses a membership change while another is not
	// yet committed, or before the term's no-op is.
	codeChangePending errs.Code = core.CodeMembershipChangePending
	// codeMembershipInvalid refuses a change, a transfer or a target the
	// current configuration does not allow.
	codeMembershipInvalid errs.Code = core.CodeMembershipInvalid
	// codeRetired refuses a node id a Remove retired for good.
	codeRetired errs.Code = core.CodeNodeIDRetired
	// codePrecondition refuses an admin operation whose expected state is
	// not the current one.
	codePrecondition errs.Code = core.CodeAdminPreconditionFailed
	// codeTransferFailed ends a transfer that did not complete in time.
	codeTransferFailed errs.Code = core.CodeTransferFailed
	// codeVersion refuses an entry kind or a payload this node cannot read.
	codeVersion errs.Code = core.CodeVersionUnsupported
	// codeMisconfigured refuses a timing contract that cannot hold.
	codeMisconfigured errs.Code = core.CodeConsensusMisconfigured
)

// becomeLeader takes leadership of the current term: a fresh progress per
// member, no lease and no read until the term's no-op, which it appends and
// broadcasts at once.
func (r *Raft) becomeLeader() {
	r.role = core.RoleLeader
	r.lead = r.id
	r.votes = map[core.NodeID]bool{}
	r.prs = map[core.NodeID]*progress{}
	for _, m := range r.conf().Members {
		r.prs[m.ID] = &progress{next: r.log.lastIndex() + 1, lastHeard: r.now}
	}
	if pr := r.prs[r.id]; pr != nil {
		pr.match = r.log.stable
		pr.facts |= peerHeard | r.announces()
	}
	r.facts &^= raftLeaseDropped
	r.leaseStart = time.Time{}
	r.rounds, r.pendingReads, r.waitingReads = nil, nil, nil
	r.transferTo = 0
	// A new leader commits a no-op of its term before it serves a read or
	// accepts a membership change (D6).
	noop := core.EntryValue{Kind: core.EntryNoOp, BodySum: wire.BodySum(nil)}
	r.appendAsLeader([]core.EntryValue{noop})
	r.termStart = r.log.lastIndex()
	r.heartbeatDue = r.now
	r.broadcastAppend(false)
}

// appendAsLeader assigns positions and chain values at the leader's
// serialized append point (D4) and appends.
func (r *Raft) appendAsLeader(es []core.EntryValue) []core.PositionValue {
	pos := make([]core.PositionValue, len(es))
	chain := r.log.chain(r.log.lastIndex())
	idx := r.log.lastIndex()
	for i := range es {
		idx++
		es[i].Position = core.PositionValue{Term: r.term, Index: idx}
		es[i].ChainSum = wire.ChainSum(chain, &es[i])
		chain = es[i].ChainSum
		pos[i] = es[i].Position
	}
	r.appendEntries(es)
	return pos
}

// tickLeader runs the leader's timers: CheckQuorum, the transfer deadline,
// then the heartbeat, in that order, so a deposed leader sends nothing.
func (r *Raft) tickLeader(now time.Time) {
	// CheckQuorum (D5): a leader that has not heard from a majority within
	// the minimum election timeout steps down.
	if !r.quorumActive(now, r.timing.ElectionTimeoutMin) {
		r.becomeFollower(r.term, 0)
		return
	}
	if r.transferTo != 0 && !now.Before(r.transferDeadline) {
		r.rd.Transfers = append(r.rd.Transfers, TransferResult{To: r.transferTo, Code: codeTransferFailed})
		r.transferTo = 0
	}
	if !now.Before(r.heartbeatDue) {
		r.heartbeatDue = now.Add(r.timing.HeartbeatInterval)
		r.broadcastHeartbeat(now)
	}
}

// quorumActive reports whether a majority of the voters, this node counted,
// was heard within window before now.
func (r *Raft) quorumActive(now time.Time, window time.Duration) bool {
	n := 0
	for _, id := range r.voters() {
		if id == r.id {
			n++
			continue
		}
		if pr := r.prs[id]; pr != nil && now.Sub(pr.lastHeard) < window {
			n++
		}
	}
	return n >= r.quorum()
}

// broadcastHeartbeat opens a confirmation round, keeping the last 64, and
// sends it to every peer with a commit clamped to the peer's match, so a
// heartbeat never commits an entry the peer may not hold.
func (r *Raft) broadcastHeartbeat(now time.Time) {
	r.nextRound++
	rd := &round{id: r.nextRound, sentAt: now, acks: map[core.NodeID]uint64{r.id: r.timing.Generation + 1}, conf: r.conf().Index}
	r.rounds = append(r.rounds, rd)
	if len(r.rounds) > 64 {
		r.rounds = r.rounds[len(r.rounds)-64:]
	}
	for _, id := range r.peers() {
		pr := r.prs[id]
		commit := min(pr.match, r.log.committed)
		if r.mutated(mutHeartbeatUnclamped) {
			commit = r.log.committed
		}
		r.send(core.MessageValue{Kind: core.MsgHeartbeat, To: id, Commit: commit, Context: rd.id})
		if pr.state == stateReplicate && pr.paused(r.cfg.MaxInflight, r.cfg.MaxInflightBytes) {
			// A full window must not stall forever on a lost
			// acknowledgement: an empty append at next (R2-07).
			if t, ok := r.log.term(pr.next - 1); ok {
				r.send(core.MessageValue{Kind: core.MsgApp, To: id, Index: pr.next - 1, LogTerm: t, Commit: min(pr.match, r.log.committed)})
			}
		}
	}
	r.confirmRounds()
}

// peers returns the other members with a progress, in configuration order,
// so that a run stays a pure function of its inputs (D15): a map's order is
// not.
func (r *Raft) peers() []core.NodeID {
	out := make([]core.NodeID, 0, len(r.prs))
	for _, m := range r.conf().Members {
		if m.ID != r.id && r.prs[m.ID] != nil {
			out = append(out, m.ID)
		}
	}
	return out
}

// broadcastAppend feeds every peer, in configuration order.
func (r *Raft) broadcastAppend(sendIfEmpty bool) {
	for _, id := range r.peers() {
		r.sendAppend(id, sendIfEmpty)
	}
}

// sendAppend feeds one peer what it lacks: entries when the log holds them,
// a fetch when their payloads were evicted, a snapshot when the log no
// longer reaches back to the peer. A paused peer gets nothing.
func (r *Raft) sendAppend(to core.NodeID, sendIfEmpty bool) {
	pr := r.prs[to]
	if pr == nil || pr.paused(r.cfg.MaxInflight, r.cfg.MaxInflightBytes) {
		return
	}
	if kept := r.cfg.Log.KeptIndex; pr.next <= kept && !r.mutated(mutKeptAppended) {
		// The entries Recover kept carry the predecessor's identity, which
		// only a snapshot of this lineage tells a peer: appended, they were
		// applied there under the new one (#507). The driver sends one at or
		// past the kept index.
		pr.becomeSnapshot(max(r.log.snapIndex, kept))
		r.rd.SendSnapshot = append(r.rd.SendSnapshot, to)
		return
	}
	prev := pr.next - 1
	prevTerm, ok := r.log.term(prev)
	if !ok {
		if r.log.snapIndex == 0 {
			return
		}
		pr.becomeSnapshot(r.log.snapIndex)
		r.rd.SendSnapshot = append(r.rd.SendSnapshot, to)
		return
	}
	if pr.next <= r.log.evicted && pr.next <= r.log.lastIndex() {
		// The payloads were evicted: the driver reads them off the loop, no
		// more of them than one append carries (#683).
		pr.facts |= peerFetching
		hi := min(r.log.evicted, r.log.lastIndex(), prev+fetchSpan(r.cfg.MaxBatchBytes)) + 1
		r.rd.Fetch = append(r.rd.Fetch, FetchSpec{To: to, Lo: pr.next, Hi: hi})
		return
	}
	ents := r.log.slice(pr.next, r.log.lastIndex()+1, r.cfg.MaxBatchBytes)
	if len(ents) == 0 && !sendIfEmpty {
		return
	}
	r.sendEntries(pr, to, prev, prevTerm, ents)
}

// Fetched hands back entries read for a peer. Anything stale — another
// role, another next, a term the log no longer has at that index — is
// dropped, and a compacted read moves the peer to the snapshot path.
func (r *Raft) Fetched(to core.NodeID, lo uint64, ents []core.EntryValue, compacted bool) {
	pr := r.prs[to]
	if pr == nil || pr.facts&peerFetching == 0 {
		return
	}
	pr.facts &^= peerFetching
	if r.role != core.RoleLeader || pr.next != lo || pr.state == stateSnapshot {
		r.sendAppend(to, false)
		return
	}
	if compacted {
		pr.becomeSnapshot(r.log.snapIndex)
		r.rd.SendSnapshot = append(r.rd.SendSnapshot, to)
		return
	}
	if !r.fetchedMatch(lo, ents) {
		r.sendAppend(to, false)
		return
	}
	prevTerm, ok := r.log.term(lo - 1)
	if !ok || len(ents) == 0 {
		r.sendAppend(to, false)
		return
	}
	// The store bounded its read by the payloads alone: the batch is cut to
	// MaxBatchBytes of its encoding, the bound slice holds the log's to, or
	// the follower refuses the frame above its MaxFrameBytes at every probe
	// and never catches up (#683). The rest is fetched again from the next
	// index.
	r.sendEntries(pr, to, lo-1, prevTerm, ents[:batchLen(ents, r.cfg.MaxBatchBytes)])
}

// fetchedMatch reports whether ents are the entries from lo on, one per
// index, at the terms the log still holds: a read that raced a truncation
// is not sent.
func (r *Raft) fetchedMatch(lo uint64, ents []core.EntryValue) bool {
	for i := range ents {
		if t, ok := r.log.term(ents[i].Position.Index); !ok || t != ents[i].Position.Term || ents[i].Position.Index != lo+uint64(i) {
			return false
		}
	}
	return true
}

// sendEntries sends one append after prev and accounts for it: a replicating
// peer's window grows by it, a probing peer pauses until it answers.
func (r *Raft) sendEntries(pr *progress, to core.NodeID, prev, prevTerm uint64, ents []core.EntryValue) {
	m := core.MessageValue{Kind: core.MsgApp, To: to, Index: prev, LogTerm: prevTerm, Entries: ents, Commit: r.log.committed}
	r.send(m)
	switch pr.state {
	case stateReplicate:
		if n := len(ents); n > 0 {
			last := ents[n-1].Position.Index
			size := 0
			for i := range ents {
				size += len(ents[i].Data) + 64
			}
			pr.next = last + 1
			pr.add(last, size)
		}
	case stateProbe:
		pr.facts |= peerProbeSent
	default:
		// stateSnapshot: a peer in a transfer is paused, so no append
		// reaches it and there is no window to account.
	}
}

// maybeCommit advances the commit to the highest index a quorum of voters
// matches, if that entry is of the leader's own term; it reports whether the
// commit moved.
func (r *Raft) maybeCommit() bool {
	vs := r.voters()
	if len(vs) == 0 {
		return false
	}
	ms := make([]uint64, 0, len(vs))
	for _, id := range vs {
		if pr := r.prs[id]; pr != nil {
			ms = append(ms, pr.match)
		} else {
			ms = append(ms, 0)
		}
	}
	slices.Sort(ms)
	idx := ms[len(ms)-r.quorum()]
	if idx <= r.log.committed {
		return false
	}
	// Only an entry of the leader's own term is committed by counting
	// replicas (Figure 8).
	if t, _ := r.log.term(idx); t != r.term && !r.mutated(mutCommitOldTermByCount) {
		return false
	}
	r.commitTo(idx)
	r.maybeSendTimeoutNow()
	return true
}

// stepLeader handles a message of the leader's term. Any answer from a
// member counts as contact for CheckQuorum, the lease and promotion.
func (r *Raft) stepLeader(now time.Time, m core.MessageValue) {
	pr := r.prs[m.From]
	switch m.Kind {
	case core.MsgReadIndex:
		r.pendingReads = append(r.pendingReads, readReq{ctx: m.Context, from: m.From})
		r.serveLeaseRead(m.Flags&core.FlagNoLease == 0)
		return
	case core.MsgVoteResp, core.MsgPreVoteResp:
		return
	default:
		// Every other kind is from a member: its progress is read below.
	}
	if pr == nil {
		return
	}
	pr.lastHeard = now
	pr.facts |= peerHeard
	// The announcement follows the peer's last message: a member rolled
	// back to a previous release stops carrying it at once.
	pr.facts = pr.facts&^(peerProtocol2|peerProtocol3) | announced(m.Flags)
	switch m.Kind {
	case core.MsgAppResp:
		r.handleAppResp(pr, m)
	case core.MsgHeartbeatResp:
		r.handleHeartbeatResp(pr, m)
	default:
		// Nothing else carries news for a leader of this term: the
		// contact above is all it records.
	}
}

// handleHeartbeatResp acknowledges every round up to the one answered,
// recording the contract generation the peer applied, follows a silent
// peer's report, and refills a window that a lost append left stalled.
//
// A probing peer behind the log is probed again even when the leader has
// nothing past its next to append: the probe of a learner sent as its
// admission committed reaches a joiner whose transport does not admit the
// leader yet, and is dropped. Resent only with entries, it waited for the
// next write, and in a quiet cluster the learner was never fed, so its
// promotion, which waits for it to hold its admission, was refused for as
// long as nothing was written (#722).
//
// A probe still in flight is probed behind, never sent again: an answer
// on the control lane overtakes it on a link narrower than one batch per
// heartbeat interval, and the copy queued behind it at every answer took
// the link from what the peer lacked (#767). A silent peer answers no
// append, so its report alone tells whether the probe reached it, and it
// is sent the batch at its next as before: followSilentReport holds it in
// probe, never in replicate, where its full window was sent nothing (#774).
func (r *Raft) handleHeartbeatResp(pr *progress, m core.MessageValue) {
	r.ackRounds(m.From, m.Context, m.Contract)
	r.confirmRounds()
	silent := m.Flags&core.FlagTruncated != 0 && !r.mutated(mutSilentReportIgnored)
	if silent {
		r.followSilentReport(pr, m)
	}
	probing := pr.state == stateProbe && pr.facts&peerProbeSent != 0 && !silent
	if pr.state == stateProbe {
		pr.facts &^= peerProbeSent
	}
	r.watchWindow(pr, m.From, m.Index)
	r.refeed(pr, m.From, probing)
}

// watchWindow probes a replicating peer whose window did not move since its
// previous heartbeat answer (probeIfLost), held the index this answer
// reports, and records the match the next answer is judged against.
func (r *Raft) watchWindow(pr *progress, to core.NodeID, held uint64) {
	if pr.state == stateReplicate && len(pr.inflight) > 0 && pr.match == pr.hbMatch {
		r.probeIfLost(pr, to, held)
	} else {
		pr.heldSince = time.Time{}
	}
	pr.hbMatch = pr.match
}

// refeed feeds a peer behind the log on its heartbeat answer: behind the
// probe still in flight to it when probing, the answer having brought no
// news of it, and what it lacks otherwise.
func (r *Raft) refeed(pr *progress, to core.NodeID, probing bool) {
	if pr.match >= r.log.lastIndex() {
		return
	}
	if probing && r.probeBehind(pr, to) {
		pr.facts |= peerProbeSent
		return
	}
	r.sendAppend(to, pr.state == stateProbe)
}

// probeBehind sends the peer an empty append after next-1, at the end of
// what is in flight to it, and reports whether it could. Appends travel on
// the data lane, in order, so it reaches the peer only once everything sent
// before it has: accepted, its acknowledgement covers what was in flight;
// refused, an append before it was lost, and the refusal backs the peer
// off from its hint (handleAppReject), as a probe did at once before (R1-11,
// #767). It carries no entry, so a link narrower than one batch per
// heartbeat interval carries it at almost no cost.
func (r *Raft) probeBehind(pr *progress, to core.NodeID) bool {
	t, ok := r.log.term(pr.next - 1)
	if !ok {
		return false
	}
	r.send(core.MessageValue{Kind: core.MsgApp, To: to, Index: pr.next - 1, LogTerm: t, Commit: r.log.committed})
	return true
}

// followSilentReport takes the heartbeat answer of a peer that truncated
// acknowledged entries and stays silent (#636). Its match is lowered to the
// durable log the answer reports, as on its refusal of an append: a match
// that still counts the entries the truncation dropped is not counted again.
// A probe then resumes past the commit the answer reports, through which the
// peer holds this leader's log. The peer acknowledges no append while
// silent, so nothing else moves its probe, and it needs this leader's
// commit to vote again: resent the same batch at every heartbeat, it stayed
// silent for as long as this leader led. Neither field ever raises match.
//
// A peer still replicating is probed too, its match kept: no
// acknowledgement will move its window again. Held in replicate with that
// window full, it was sent only the empty append probeBehind sends after
// the window's end, which it accepted in silence, and never an entry of
// this leader's term, so it stayed silent for as long as this leader led
// (#774). Only the transport's report of a failure to reach it moved it to
// probe, and nothing ensures one reaches the leader. The probe resumes past
// the commit the answer reports, so the window it already holds is not
// sent again.
func (r *Raft) followSilentReport(pr *progress, m core.MessageValue) {
	if pr.state == stateSnapshot {
		return
	}
	if m.RejectHint < pr.match {
		pr.match = m.RejectHint
		pr.becomeProbe()
	} else if pr.state == stateReplicate {
		pr.becomeProbe()
	}
	if pr.state == stateProbe && m.Commit >= pr.next && m.Commit <= r.log.lastIndex() {
		pr.next = m.Commit + 1
	}
}

// probeIfLost probes a peer whose window did not move across two heartbeat
// answers: an append in it may be lost, the transport may drop, and a
// window that never refills must not stall (R1-11). A follower that
// reports holding the oldest append in flight may only acknowledge it
// late, after its fdatasync: probing it resent, then refilled, a window it
// held (#560). It has ElectionTimeoutMax for it. Past that, its
// acknowledgement was lost too, by a dropped frame or a restart after the
// write, and the window, full, never moved again. held 0 is a follower
// that reports nothing.
//
// A follower that reports holding less may still be receiving the window:
// its answer crossed the control lane while a batch longer than a heartbeat
// interval to send was crossing the data lane. Moved to probe, it was sent
// the window again from match+1 behind what the lane still carried, at
// every answer, and on a link narrower than one batch per heartbeat
// interval it never caught up (#767). It is probed behind the window
// instead, whose refusal finds a lost append.
func (r *Raft) probeIfLost(pr *progress, to core.NodeID, held uint64) {
	if held >= pr.inflight[0] {
		if pr.heldSince.IsZero() {
			pr.heldSince = r.now
		}
		if r.now.Sub(pr.heldSince) <= r.timing.ElectionTimeoutMax {
			return
		}
	} else if r.probeBehind(pr, to) {
		pr.heldSince = time.Time{}
		return
	}
	pr.heldSince = time.Time{}
	pr.becomeProbe()
}

// ackRounds records from's answer to every round up to round, with the
// contract generation it applied, stored plus one so that 0 stays "no
// answer".
func (r *Raft) ackRounds(from core.NodeID, round, contract uint64) {
	for _, rd := range r.rounds {
		if rd.id <= round {
			rd.acks[from] = contract + 1
		}
	}
}

// handleAppResp moves a peer's progress on an append's answer, commits what
// a quorum now holds, and feeds the peer what it still lacks.
func (r *Raft) handleAppResp(pr *progress, m core.MessageValue) {
	if m.Reject {
		r.handleAppReject(pr, m)
		return
	}
	if m.Index > r.log.lastIndex() && !r.mutated(mutMatchPastLastIndex) {
		// No honest follower holds past this leader's log (Leader
		// Completeness): taken as match, the index of a follower whose
		// snapshot metadata rotted made every write committed on the
		// leader's disk alone (#546). Ignored.
		return
	}
	switch {
	case pr.maybeUpdate(m.Index):
		r.ackMoved(pr, m.Index)
	case pr.state == stateProbe && m.Index == pr.match && m.Index+1 >= pr.next:
		// The empty append probeBehind sent after the match, accepted:
		// the probe before it was lost, and the peer holds the leader's
		// log through its match (#767).
		pr.becomeReplicate()
	default:
		pr.freeTo(m.Index)
	}
	if pr.match < r.log.lastIndex() {
		r.sendAppend(m.From, false)
	}
}

// ackMoved moves a peer whose match an answer raised through index: out of
// a probe the answer settles, or out of a snapshot it covers, then commits
// what a quorum now holds.
func (r *Raft) ackMoved(pr *progress, index uint64) {
	switch {
	case pr.state == stateProbe && index+1 >= pr.next:
		pr.becomeReplicate()
	case pr.state == stateProbe:
		// An answer to an append sent before a snapshot transfer, late: it
		// does not answer the probe after the snapshot, and taken as one it
		// moved the peer back before the snapshot's index, which the log no
		// longer holds, and the leader sent another snapshot (#550). The
		// probe goes on from where it was.
	case pr.state == stateSnapshot && pr.match >= pr.pendingSnapshot:
		pr.becomeProbe()
		pr.becomeReplicate()
	}
	pr.freeTo(index)
	if r.maybeCommit() {
		r.broadcastAppend(true)
	}
	r.maybeSendTimeoutNow()
}

// handleAppReject backs a peer off after a refused append: to a snapshot it
// asked for, to the truncation it reports, or to the hint of a rejection
// that still answers an outstanding append; any other rejection is stale and
// ignored.
func (r *Raft) handleAppReject(pr *progress, m core.MessageValue) {
	if m.Flags&core.FlagNeedSnapshot != 0 && pr.state != stateSnapshot {
		pr.becomeSnapshot(r.log.snapIndex)
		r.rd.SendSnapshot = append(r.rd.SendSnapshot, m.From)
		return
	}
	if m.Flags&core.FlagTruncated != 0 && m.RejectHint < pr.match {
		// The peer lost acknowledged entries to a truncation it reports;
		// its log is the truth now.
		pr.match = m.RejectHint
		pr.becomeProbe()
		pr.next = m.RejectHint + 1
		r.sendAppend(m.From, true)
		return
	}
	// Rejections are handled per state (R2-07): only one answering the
	// outstanding probe, or an append still in the window, counts.
	if !pr.rejectCounts(m.Index) {
		return
	}
	pr.next = max(min(m.Index, m.RejectHint+1), pr.match+1)
	if pr.state == stateReplicate {
		pr.state = stateProbe
		pr.freeAll()
	}
	pr.facts &^= peerProbeSent
	r.sendAppend(m.From, false)
	if pr.facts&peerProbeSent == 0 {
		r.sendAppend(m.From, true)
	}
}

// ---- proposals ----

// Propose appends entries at the leader. Kind, Version, BodySum and Data are
// the proposer's; positions and chain values are assigned here. A refusal
// appends nothing.
func (r *Raft) Propose(now time.Time, es []core.EntryValue) ([]core.PositionValue, errs.Code) {
	return r.propose(now, es, r.id)
}

// propose is Propose for entries received from origin: this leader, or
// the member that forwarded them, authenticated by the transport.
func (r *Raft) propose(now time.Time, es []core.EntryValue, origin core.NodeID) ([]core.PositionValue, errs.Code) {
	r.now = now
	if r.role != core.RoleLeader || r.transferTo != 0 {
		return nil, codeNotLeader
	}
	var transfer core.NodeID
	for i := range es {
		to, code := r.checkProposed(&es[i], r.log.lastIndex()+uint64(i)+1, origin)
		if code != 0 {
			return nil, code
		}
		if to != 0 {
			transfer = to
		}
	}
	pos := r.appendAsLeader(es)
	r.broadcastAppend(false)
	if transfer != 0 {
		r.startTransfer(transfer)
	}
	return pos, 0
}

// checkProposed admits one proposed entry that would land at index: a
// command as it is unless the no-space alarm is committed, a no-op or an
// unknown kind never, an engine entry once validateAdmin accepts and
// rewrites it. It returns the target of a transfer the entry asks for.
//
// While the alarm stands every command is refused NO_SPACE, a forwarded one
// too, and the engine entries — the alarm, its clearing, a membership
// change, a transfer — are still admitted, so the cluster can be repaired
// (D17, R2-29). It used to admit every command (#515).
func (r *Raft) checkProposed(e *core.EntryValue, index uint64, origin core.NodeID) (core.NodeID, errs.Code) {
	switch {
	case e.Kind == core.EntryCommand:
		if r.noSpace != 0 && !r.mutated(mutNoSpaceAdmits) {
			return 0, core.CodeNoSpace
		}
		return 0, 0
	case e.Kind == core.EntryNoOp || !e.Kind.Known():
		return 0, codeVersion
	default:
		return r.validateAdmin(e, index, origin)
	}
}

// validateAdmin re-validates an engine entry against the leader's own state
// and rewrites a membership change into the whole configuration (D8).
//
// Once the committed cluster version is 2 it records in the entry the node
// it received the operation from, overwriting whatever the proposer wrote:
// the ORIGIN of D8 is the TLS identity of the forwarding connection, never
// a field the forwarder wrote. Before, the entry is of payload version 0,
// which carries none, so a node of protocol 1 still reads it (#561). Once
// it is 3 the entry is of payload version 3, which carries the
// configuration's retired keys as well (#648).
func (r *Raft) validateAdmin(e *core.EntryValue, index uint64, origin core.NodeID) (core.NodeID, errs.Code) {
	admin, err := wire.ParseAdminAt(e.Version, e.Data)
	if err != nil {
		return 0, codeVersion
	}
	transfer, code := r.checkAdmin(e.Kind, &admin, index, origin)
	if code != 0 {
		return 0, code
	}
	switch {
	case r.mutated(mutOriginAsWritten):
		// The payload stays as the proposer wrote it.
	case r.atProtocol3():
		e.Version, admin.Origin = wire.AdminVersion3, origin
	case r.clusterVer >= Protocol2:
		e.Version, admin.Origin = wire.AdminVersion2, origin
	default:
		e.Version, admin.Origin = 0, 0
	}
	e.Data = wire.AppendAdminAt(nil, e.Version, &admin)
	e.BodySum = wire.BodySum(e.Data)
	return transfer, 0
}

// checkAdmin holds an engine entry of kind to the leader's state, rewriting
// admin in place where the entry it replicates differs from the one
// proposed. origin is the node the leader received it from, authenticated
// by the transport. It returns the transfer target of an EntryTransfer.
func (r *Raft) checkAdmin(kind core.EntryKind, admin *wire.AdminValue, index uint64, origin core.NodeID) (core.NodeID, errs.Code) {
	switch kind {
	case core.EntryMembership:
		return 0, r.checkMembership(admin, index, origin)
	case core.EntryTimingContract:
		return 0, r.checkTimingContract(admin)
	case core.EntryClusterVersion:
		return 0, r.checkClusterVersion(admin)
	case core.EntryNoSpaceClear:
		return 0, r.checkNoSpaceClear(admin)
	case core.EntryTransfer:
		return r.checkTransfer(admin)
	default:
		// EntryNoSpaceRaise has no precondition: any member may raise the
		// alarm. Propose never hands a command, a no-op or an unknown kind
		// here.
		return 0, 0
	}
}

// checkMembership admits one membership change at a time, only once the
// term's no-op committed, and rewrites it into the whole configuration that
// takes effect at index. origin is the node the change came from.
func (r *Raft) checkMembership(admin *wire.AdminValue, index uint64, origin core.NodeID) errs.Code {
	if (!r.mutated(mutMembershipBeforeNoOp) && r.log.committed < r.termStart) || r.conf().Index > r.log.committed {
		return codeChangePending
	}
	next, code := r.nextConf(*admin, origin)
	if code != 0 {
		return code
	}
	next.Index = index
	admin.Membership = next
	return 0
}

// checkTimingContract admits a contract proposed against the current
// generation that can hold: a heartbeat at most a fifth of the minimum
// election timeout, a non-empty randomization range and a drift in [0, 1).
// Admitting it drops the lease at once (D9 d).
func (r *Raft) checkTimingContract(admin *wire.AdminValue) errs.Code {
	if !admin.Pre.Log.Same(r.cfg.Log) || admin.Pre.Generation != r.timing.Generation {
		return codePrecondition
	}
	c := admin.Contract
	// The drift is checked as a range that NaN is outside of: a NaN drift
	// compared false both ways and was admitted (#523).
	if c.HeartbeatInterval <= 0 || c.ElectionTimeoutMin <= 0 || c.HeartbeatInterval*5 > c.ElectionTimeoutMin ||
		c.ElectionTimeoutMax <= c.ElectionTimeoutMin || !(c.MaxClockDrift >= 0 && c.MaxClockDrift < 1) {
		return codeMisconfigured
	}
	// From the moment a new contract is proposed, the lease is dropped
	// (D9 d).
	r.facts |= raftLeaseDropped
	return 0
}

// checkClusterVersion admits only a raise, proposed against the current
// cluster generation of this log.
func (r *Raft) checkClusterVersion(admin *wire.AdminValue) errs.Code {
	if admin.ClusterVersion > r.speaks() {
		// No member of this version announces more (D18, review M3).
		return codeVersion
	}
	if !admin.Pre.Log.Same(r.cfg.Log) || admin.Pre.Generation != r.clusterGen || admin.ClusterVersion <= r.clusterVer {
		return codePrecondition
	}
	if r.mutated(mutRaiseUnannounced) {
		return 0
	}
	if code := r.checkAnnounced(admin.ClusterVersion); code != 0 {
		return code
	}
	return r.checkHeard(admin.ClusterVersion)
}

// checkAnnounced refuses a raise to v while a member of the latest
// configuration, voter or learner, was not heard in this term announcing
// it: the operation is refused while any member does not speak the version
// (D18). A member of a previous release would stop on the raise, and on
// every format written after it. A node of protocol 3 announces 2 as well.
func (r *Raft) checkAnnounced(v uint32) errs.Code {
	if v < Protocol2 {
		return 0
	}
	want := peerHeard | peerProtocol2
	if v >= Protocol3 {
		want |= peerProtocol3
	}
	for _, m := range r.conf().Members {
		if pr := r.prs[m.ID]; pr == nil || pr.facts&want != want {
			return codeVersion
		}
	}
	return 0
}

// checkHeard refuses a raise to v while a member of the latest
// configuration, voter or learner, was last heard a vote window ago or
// more, whatever its last message announced: an announcement holds for as
// long as the member answers, not for the rest of the term. A voter that
// announced the version once and then went down was rolled back to the
// previous release meanwhile; it met the raise only as a snapshot it could
// not read once the leader compacted past its log, and left replication
// without a word (#665). The window is answering's, the minimum election
// timeout, which the leader's heartbeats fill several times over. It is
// apart from checkAnnounced, which inlines.
func (r *Raft) checkHeard(v uint32) errs.Code {
	if v < Protocol2 || r.mutated(mutRaiseOverSilence) {
		return 0
	}
	for _, m := range r.conf().Members {
		if m.ID != r.id && !r.answering(m.ID) {
			return codeVersion
		}
	}
	return 0
}

// announcementOf is the announcement every frame of a node speaking
// protocol version v carries: FlagProtocol2 from 2, and FlagProtocol3 beside
// it from 3 (D18, #648).
func announcementOf(v uint32) core.MessageFlags {
	var f core.MessageFlags
	if v >= Protocol2 {
		f |= core.FlagProtocol2
	}
	if v >= Protocol3 {
		f |= core.FlagProtocol3
	}
	return f
}

// announces is the announcement this node's own frames carry, as the facts
// a leader keeps of a peer that sent them.
func (r *Raft) announces() peerFacts {
	return announced(announcementOf(r.speaks()))
}

// announced is the announcement a message's flags carry.
func announced(flags core.MessageFlags) peerFacts {
	var f peerFacts
	if flags&core.FlagProtocol2 != 0 {
		f |= peerProtocol2
	}
	if flags&core.FlagProtocol3 != 0 {
		f |= peerProtocol3
	}
	return f
}

// checkNoSpaceClear admits clearing only the alarm that is raised now, so a
// late clear cannot end a newer alarm.
func (r *Raft) checkNoSpaceClear(admin *wire.AdminValue) errs.Code {
	if !admin.Pre.Log.Same(r.cfg.Log) || r.noSpace == 0 || admin.Pre.Alarm != r.noSpace {
		return codePrecondition
	}
	return 0
}

// checkTransfer admits a transfer to another voter and returns it.
func (r *Raft) checkTransfer(admin *wire.AdminValue) (core.NodeID, errs.Code) {
	if admin.Target == r.id || !r.isVoter(admin.Target) {
		return 0, codeMembershipInvalid
	}
	return admin.Target, 0
}

// nextConf computes the configuration a single-server change produces, and
// refuses it on its merits (D8). origin is the node the change came from,
// authenticated by the transport. On a refusal the configuration returned
// is not to be used.
func (r *Raft) nextConf(admin wire.AdminValue, origin core.NodeID) (core.MembershipValue, errs.Code) {
	cur := r.conf().Clone()
	idx := slices.IndexFunc(cur.Members, func(m core.MemberValue) bool { return m.ID == admin.Target })
	var code errs.Code
	switch admin.Change {
	case wire.ChangeAddLearner:
		code = r.addLearner(&cur, &admin)
	case wire.ChangePromote:
		code = r.promote(&cur, admin.Target, idx)
	case wire.ChangeRemove:
		code = r.removeMember(&cur, admin.Target, idx)
	case wire.ChangeUpdateAddress:
		code = r.updateAddress(&cur, &admin, idx)
	case wire.ChangeDenyPin:
		code = r.denyPin(&cur, admin.Target, idx)
	case wire.ChangeSnapshotLimit:
		code = r.updateSnapshotLimit(&cur, &admin, idx, origin)
	default:
		code = codeMembershipInvalid
	}
	return cur, code
}

// addLearner adds the one member admin carries as a learner: a new id with
// a key no member holds and none held before it, never retired, with a pin,
// and only while a quorum answers so the change can commit.
func (r *Raft) addLearner(cur *core.MembershipValue, admin *wire.AdminValue) errs.Code {
	if len(admin.Membership.Members) != 1 {
		return codeMembershipInvalid
	}
	nm := admin.Membership.Members[0]
	if cur.IsRetired(nm.ID) {
		return codeRetired
	}
	var unpinned [32]byte
	if nm.ID == 0 || nm.SPKI == unpinned {
		return codeMembershipInvalid
	}
	// One id, one member; and one key, one member, since the transport
	// admits by key (review M5). And one key, one id for the cluster's
	// whole life: a key retired with a member is never taken again under a
	// new id. A node whose configuration still listed the old member under
	// it refused the new one as leader, a stale voter as a joiner, for as
	// long as it led (#648).
	if _, ok := cur.Member(nm.ID); ok || slices.ContainsFunc(cur.Members, func(m core.MemberValue) bool { return m.SPKI == nm.SPKI }) ||
		(cur.IsRetiredKey(nm.SPKI) && !r.mutated(mutRetiredKeyAdmitted)) {
		return codeMembershipInvalid
	}
	if !r.quorumActive(r.now, r.voteWindow) {
		return codeMembershipInvalid
	}
	nm.Voter = false
	cur.Members = append(cur.Members, nm)
	return 0
}

// promote makes the learner at idx a voter, only once it caught up, holds
// the configuration that admitted it and was heard recently, and only if
// the new voter set still answers by a majority.
func (r *Raft) promote(cur *core.MembershipValue, target core.NodeID, idx int) errs.Code {
	if cur.IsRetired(target) {
		return codeRetired
	}
	if idx < 0 || cur.Members[idx].Voter {
		return codeMembershipInvalid
	}
	pr := r.prs[target]
	// Recent DURABLE progress within PromoteLag of the commit, heard
	// within the last election timeout (D8). The progress reaches the
	// entry that admitted the learner as well: a joiner's stamp turns a
	// member's only once its log holds that entry (#695), and until then
	// it refuses every vote. Within PromoteLag of a young commit, a
	// learner heard at match 0 was promoted, and once the leader failed
	// the voters left could elect no one (#722). An acknowledgement
	// leaves after the sync that covers it (D5b), so the learner holds
	// its admission on disk.
	if pr == nil || pr.facts&peerHeard == 0 || pr.match+r.cfg.PromoteLag < r.log.committed ||
		pr.match < r.admittedAt(target) || r.now.Sub(pr.lastHeard) >= r.timing.ElectionTimeoutMax {
		return codeMembershipInvalid
	}
	cur.Members[idx].Voter = true
	cur.Members[idx].Joining = false
	if !r.activeMajority(*cur) {
		return codeMembershipInvalid
	}
	return 0
}

// admittedAt is the index of the configuration that admitted id, as this
// leader knows it: the first appended one that lists it, or the snapshot
// base's when the base already does, the admission compacted at or below
// it. An ID is never reused, so the first configuration that lists it is
// its admission. The latest one, which the caller found id in, is the
// last resort.
func (r *Raft) admittedAt(id core.NodeID) uint64 {
	if _, ok := r.base.Member(id); ok {
		return r.base.Index
	}
	for k := range r.confs {
		if _, ok := r.confs[k].m.Member(id); ok {
			return r.confs[k].index
		}
	}
	return r.conf().Index
}

// removeMember removes the member at idx, never this leader, and retires
// its id, and from protocol 3 its key; a voter's removal may not cross
// MinVoters nor leave a voter set without an answering majority.
func (r *Raft) removeMember(cur *core.MembershipValue, target core.NodeID, idx int) errs.Code {
	if idx < 0 || target == r.id {
		return codeMembershipInvalid
	}
	wasVoter := cur.Members[idx].Voter
	r.retireKey(cur, cur.Members[idx].SPKI)
	cur.Members = slices.Delete(cur.Members, idx, idx+1)
	cur.Retired = append(cur.Retired, target)
	if wasVoter {
		n := len(cur.Voters())
		if n == 0 || n < r.cfg.MinVoters {
			return codeMembershipInvalid
		}
	}
	if !r.activeMajority(*cur) || r.removalStrands(cur, target, wasVoter) {
		return codeMembershipInvalid
	}
	return 0
}

// removalStrands reports whether removing target, which leaves next, may
// strand a voter: only the removal of a member that answers, and that may
// vouch for a voter — a voter, or a member that ever was one.
func (r *Raft) removalStrands(next *core.MembershipValue, target core.NodeID, wasVoter bool) bool {
	if r.mutated(mutRemoveStrands) || !r.answering(target) || (!wasVoter && r.neverVoter(target)) {
		return false
	}
	return r.strands(next)
}

// neverVoter reports whether id is a learner in every configuration this
// leader retains that lists it, the snapshot base's included. A member is
// promoted, never demoted, so it was a learner in every configuration
// before those as well, and vouched counts it for no voter, whatever
// configuration that voter holds: its removal strands no one. strands
// alone cannot tell, since a voter it already finds stranded stays so, and
// every removal of an answering learner was refused while a founder listed
// before the leader was silent over a compacted log (#614).
func (r *Raft) neverVoter(id core.NodeID) bool {
	if m, ok := r.base.Member(id); ok && m.Voter {
		return false
	}
	for k := range r.confs {
		if m, ok := r.confs[k].m.Member(id); ok && m.Voter {
			return false
		}
	}
	return true
}

// answering reports whether peer id answered within the vote window.
func (r *Raft) answering(id core.NodeID) bool {
	pr := r.prs[id]
	return pr != nil && pr.facts&peerHeard != 0 && r.now.Sub(pr.lastHeard) < r.voteWindow
}

// strands reports whether next leaves a voter unable to hear this leader
// once it comes back: a voter admits on its own configuration, and when
// every voter it knows has left, no member of it can vouch for this leader
// (#542) and its transport refuses this leader's key. A voter of {A, B, C}
// down while B and C were replaced by D and E came back as a voter of
// {A, D, E} that heard nobody, and after one more failure A and D, two
// running voters of three, never committed again (#603).
//
// The guard admits no one: it only refuses a removal, or a move (#761), of
// a member that answers, which would take away the last voter such a voter
// knows at the address it serves. A member that does not answer cannot
// vouch for anyone now, and its removal or move is never refused here, so
// a dead voter can always be removed, or moved.
func (r *Raft) strands(next *core.MembershipValue) bool {
	for _, m := range next.Members {
		if m.Voter && m.ID != r.id && !r.reachedBy(next, m.ID) {
			return true
		}
	}
	return false
}

// reachedBy reports whether voter v, whatever configuration of its past it
// holds, still knows in next this leader or a voter that would vouch for
// it. A voter that answers, heard at a match this leader's log still
// resolves, holds that configuration or a later one: every member of next
// it lists there it lists in every later one, since an id is never added
// back once removed. A voter that no longer answers may come back over a
// truncated log (TruncateAndStaySilent), whose offline refusal reads only
// its own log and sees no change committed while it was down: a founder
// heard at {B, V, D, E}, B removed while it was down, came back silent
// over {A, B, V} and refused D and E for good (#763). For it, as for any
// other voter, every retained configuration that lists it is a candidate,
// and when it was a member before the log's start the oldest it may hold
// is unknown: only this leader listed before it, so added before it, is
// then known to it for sure.
func (r *Raft) reachedBy(next *core.MembershipValue, v core.NodeID) bool {
	if pr := r.prs[v]; pr != nil && r.answering(v) && pr.match >= r.log.snapIndex {
		return vouched(next, r.confAt(pr.match), v, r.id)
	}
	if !r.vouchedInRetained(next, v) {
		return false
	}
	if _, listed := r.base.Member(v); !listed || r.log.snapIndex == 0 {
		// Added after the log's start, or a founder of a log that was never
		// compacted: the retained configurations are every one it may hold.
		return true
	}
	return listedBefore(next, r.id, v)
}

// vouchedInRetained reports whether v is vouched for in every retained
// configuration, the snapshot base's included.
func (r *Raft) vouchedInRetained(next *core.MembershipValue, v core.NodeID) bool {
	if !vouched(next, &r.base, v, r.id) {
		return false
	}
	for k := range r.confs {
		if !vouched(next, &r.confs[k].m, v, r.id) {
			return false
		}
	}
	return true
}

// listedBefore reports whether first comes before then in next's members,
// the order members were added in.
func listedBefore(next *core.MembershipValue, first, then core.NodeID) bool {
	for _, m := range next.Members {
		switch m.ID {
		case then:
			return false
		case first:
			return true
		}
	}
	return false
}

// vouched reports whether a voter v holding known still finds in next a
// voter of known that stays, which answers its PreVote with the record of
// whichever member leads (#542). A known that does not list v is not one v
// holds, and is passed. v sends its PreVote to the voters of known alone,
// at the addresses known names: a voter next lists at another address no
// longer hears it (#761), and a member known lists as a learner is never
// asked, the leader included. Counted as a learner, the leader vouched only
// while it led: once the leadership moved to a member v does not know, v
// asked no one that follows it, and its transport refused the new leader's
// key (#765). A leader that is a voter of known is counted at any address:
// while it leads it dials v, which admits it by key.
func vouched(next, known *core.MembershipValue, v, leader core.NodeID) bool {
	if _, listed := known.Member(v); !listed {
		return true
	}
	for _, k := range known.Members {
		if k.ID == v || !k.Voter {
			continue
		}
		if m, stays := next.Member(k.ID); stays && (k.ID == leader || m.Address == k.Address) {
			return true
		}
	}
	return false
}

// updateAddress changes the address of the member at idx to the one admin
// carries, while a majority answers, unless the move may strand a voter.
func (r *Raft) updateAddress(cur *core.MembershipValue, admin *wire.AdminValue, idx int) errs.Code {
	if idx < 0 || len(admin.Membership.Members) != 1 || !r.activeMajority(*cur) {
		return codeMembershipInvalid
	}
	cur.Members[idx].Address = admin.Membership.Members[0].Address
	if r.moveStrands(cur, cur.Members[idx].ID) {
		return codeMembershipInvalid
	}
	return 0
}

// moveStrands reports whether moving target, at the address next records,
// may strand a voter, as removalStrands does for a removal: a voter that
// lists target at its old address sends it a PreVote nothing answers, and
// a voter down while the leader was admitted, C removed and then its last
// voucher B moved, came back hearing no one, refused by the leader's key,
// for as long as that leader led (#761). Only the move of a member that
// answers and may vouch is judged — a voter, or a member that ever was
// one — and never the leader's own, which vouched counts at any address.
func (r *Raft) moveStrands(next *core.MembershipValue, target core.NodeID) bool {
	if target == r.id || !r.answering(target) || r.neverVoter(target) {
		return false
	}
	return r.strands(next)
}

// updateSnapshotLimit records, for the member at idx, the MaxSnapshotBytes
// the one member admin carries names, on that member's word alone: origin,
// the node the transport authenticated, must be the target. Only a limit
// above 0 that differs from the recorded one is admitted, and only while a
// majority answers. A voter's recorded limit bounds every cut of every node
// (cutLimit), a learner's what it is offered and fed, and nothing updated
// it once its JOIN wrote it: a member that raised its limit and restarted,
// #512's remedy, was held to the old one, every cut failed
// SNAPSHOT_TOO_LARGE once the state outgrew it, and the no-space alarm
// refused every write of the cluster (#677). A change of another member's
// limit is refused: only that member knows the limit it runs with. A limit
// of 0 is refused, which would record no limit at all and lift every bound
// on what the member is offered; and the recorded one, which would append
// an entry for nothing.
func (r *Raft) updateSnapshotLimit(cur *core.MembershipValue, admin *wire.AdminValue, idx int, origin core.NodeID) errs.Code {
	if idx < 0 || len(admin.Membership.Members) != 1 || admin.Membership.Members[0].ID != admin.Target {
		return codeMembershipInvalid
	}
	limit := admin.Membership.Members[0].MaxSnapshotBytes
	if !r.limitStated(limit, cur.Members[idx].MaxSnapshotBytes, origin == admin.Target) || !r.activeMajority(*cur) {
		return codeMembershipInvalid
	}
	cur.Members[idx].MaxSnapshotBytes = limit
	return 0
}

// limitStated reports whether limit may replace recorded, the limit a
// member's record holds: stated by that member itself (byItself), above 0,
// and another one.
func (r *Raft) limitStated(limit, recorded int64, byItself bool) bool {
	return (byItself || r.mutated(mutLimitAnyOrigin)) && (limit > 0 || r.mutated(mutLimitZero)) &&
		(limit != recorded || r.mutated(mutLimitUnchanged))
}

// denyPin withdraws the pin of the member at idx, target: its connections
// are refused once the change commits, while it stays a member. Never this
// leader's own, and only while the voters that answer now, the target left
// out, are a majority: the target is cut from the moment the change commits,
// and the change itself must commit without it. Denying the pin of the one
// live voter of three left the entry needing an acknowledgement the
// transport no longer let through, and the cluster had no quorum until the
// dead voter came back (#530, R2-27).
func (r *Raft) denyPin(cur *core.MembershipValue, target core.NodeID, idx int) errs.Code {
	if idx < 0 || (!r.mutated(mutDenyPinUnchecked) && (target == r.id || !r.activeMajorityWithout(*cur, target))) {
		return codeMembershipInvalid
	}
	r.retireKey(cur, cur.Members[idx].SPKI)
	clear(cur.Members[idx].SPKI[:])
	return 0
}

// retireKey keeps spki, the key of a member that leaves the configuration's
// pins — removed, or its pin denied — once the committed cluster version is
// 3, so that no new member takes it (#648). Below 3 the configuration
// carries no retired key, a format a node of protocol 2 cannot read; and
// the zero pin a DenyPin left is no key.
func (r *Raft) retireKey(cur *core.MembershipValue, spki [32]byte) {
	var unpinned [32]byte
	switch {
	case r.mutated(mutKeyNotRetired), spki == unpinned, cur.IsRetiredKey(spki):
		return
	case r.atProtocol3():
		cur.RetiredKeys = append(cur.RetiredKeys, spki)
	default:
		// Below protocol 3 nothing new is written.
	}
}

// atProtocol3 reports whether the committed cluster version admits the
// formats of protocol 3: every member announced it before the raise
// committed, and a node of protocol 2 stops on the raise, or is withheld a
// snapshot it cannot read (D18, #665).
func (r *Raft) atProtocol3() bool {
	return r.clusterVer >= Protocol3 || r.mutated(mutProtocol3BelowRaise)
}

// activeMajority is etcd's strict reconfiguration check: the voters of m
// that answer the leader NOW form a majority of m (R2-27).
func (r *Raft) activeMajority(m core.MembershipValue) bool {
	return r.activeMajorityWithout(m, 0)
}

// activeMajorityWithout is activeMajority with silent counted as a voter
// that does not answer, whatever it did last.
func (r *Raft) activeMajorityWithout(m core.MembershipValue, silent core.NodeID) bool {
	vs := m.Voters()
	n := 0
	for _, id := range vs {
		if id == r.id {
			n++
			continue
		}
		if id == silent {
			continue
		}
		if pr := r.prs[id]; pr != nil && pr.facts&peerHeard != 0 && r.now.Sub(pr.lastHeard) < r.voteWindow {
			n++
		}
	}
	return len(vs) > 0 && n >= len(vs)/2+1
}

// fwdKey names one submission: the follower that forwarded it and its
// submission id, which never repeats across the follower's restarts (R7-23).
// Two frames with one key are one submission delivered twice.
type fwdKey struct {
	from core.NodeID
	ctx  uint64
}

// fwdAnswer is what a forward was answered: the position its entry was
// appended at, or a refusal with its code and the leader hinted.
type fwdAnswer struct {
	reject  bool
	code    errs.Code
	hint    uint64
	index   uint64
	logTerm uint64
}

// fwdTable keeps the answers given to forwards, bounded by maxFwdAnswers,
// the oldest evicted first. It outlives a term: a duplicate that arrives
// after this node changed term, or led again, is still the submission it
// answered, and replaying an old answer is harmless, the follower having
// taken the first one (#401).
type fwdTable struct {
	byKey map[fwdKey]fwdAnswer
	// order is a ring of the kept keys, oldest at next once it is full.
	order []fwdKey
	next  int
}

// lookup returns the answer kept for k.
func (a *fwdTable) lookup(k fwdKey) (fwdAnswer, bool) {
	v, ok := a.byKey[k]
	return v, ok
}

// remember keeps v for k, evicting the oldest answer when limit are kept.
func (a *fwdTable) remember(k fwdKey, v fwdAnswer, limit int) {
	if a.byKey == nil {
		a.byKey = map[fwdKey]fwdAnswer{}
	}
	if len(a.order) < limit {
		a.order = append(a.order, k)
	} else {
		delete(a.byKey, a.order[a.next])
		a.order[a.next] = k
		a.next = (a.next + 1) % limit
	}
	a.byKey[k] = v
}

// handleForward answers a forward once per submission. A transport that
// resends a batch on a fresh connection can deliver a forward twice (#377);
// proposed twice, its command would be applied twice, the follower having
// taken the first answer and dropped the second (#401). A forward already
// answered, in this term or an earlier one, is answered again with the same
// answer, position or refusal, and nothing is proposed.
func (r *Raft) handleForward(m core.MessageValue) {
	key := fwdKey{from: m.From, ctx: m.Context}
	a, ok := r.fwdAnswers.lookup(key)
	if !ok || r.mutated(mutForwardNoDedupe) {
		a = r.answerForward(m)
		r.fwdAnswers.remember(key, a, maxFwdAnswers)
	}
	resp := core.MsgForwardResp
	if m.Kind == core.MsgForwardAdmin {
		resp = core.MsgForwardAdminResp
	}
	r.send(core.MessageValue{
		Kind: resp, To: m.From, Context: m.Context,
		Reject: a.reject, Code: a.code, RejectHint: a.hint, Index: a.index, LogTerm: a.logTerm,
	})
}

// answerForward proposes the one entry a follower forwarded and returns its
// position or a refusal. The entry is copied out of the frame first, and a
// body that does not match its checksum is never proposed.
func (r *Raft) answerForward(m core.MessageValue) fwdAnswer {
	if r.role != core.RoleLeader || len(m.Entries) != 1 {
		return fwdAnswer{reject: true, code: codeNotLeader, hint: uint64(r.lead)}
	}
	e := m.Entries[0]
	e.Data = ownCopy(e.Data)
	if wire.BodySum(e.Data) != e.BodySum {
		return fwdAnswer{reject: true, code: core.CodeFrameInvalid}
	}
	if (m.Kind == core.MsgForward) != (e.Kind == core.EntryCommand) {
		return fwdAnswer{reject: true, code: codeVersion}
	}
	if e.Kind == core.EntryCommand {
		if code := r.admitForward(len(e.Data)); code != 0 {
			return fwdAnswer{reject: true, code: code}
		}
	}
	pos, code := r.propose(r.now, []core.EntryValue{e}, m.From)
	if code != 0 {
		return fwdAnswer{reject: true, code: code, hint: uint64(r.lead)}
	}
	if e.Kind == core.EntryCommand {
		r.fwd = append(r.fwd, fwdEntry{index: pos[0].Index, size: int64(len(e.Data))})
		r.fwdBytes += int64(len(e.Data))
	}
	return fwdAnswer{index: pos[0].Index, logTerm: pos[0].Term}
}

// admitForward applies to a forwarded command the bounds the driver applies
// to a local one before anything is appended: the entry size, the bytes
// appended and not committed, the apply backpressure (D6, review M2).
func (r *Raft) admitForward(size int) errs.Code {
	if r.fwdTerm != r.term {
		r.fwd, r.fwdBytes, r.fwdTerm = r.fwd[:0], 0, r.term
	}
	k := 0
	for k < len(r.fwd) && r.fwd[k].index <= r.log.committed {
		r.fwdBytes -= r.fwd[k].size
		k++
	}
	r.fwd = r.fwd[k:]
	switch {
	case r.cfg.MaxEntryBytes > 0 && size > r.cfg.MaxEntryBytes:
		return core.CodeEntryTooLarge
	case r.facts&raftOverloaded != 0, r.cfg.MaxForwardBytes > 0 && r.fwdBytes+int64(size) > r.cfg.MaxForwardBytes:
		return core.CodeOverloaded
	}
	return 0
}

// ---- reads ----

// ReadIndex asks for a linearizable read point under ctx. noLease forces a
// confirmation round.
func (r *Raft) ReadIndex(now time.Time, ctx uint64, noLease bool) {
	r.now = now
	switch {
	case r.role == core.RoleLeader:
		r.pendingReads = append(r.pendingReads, readReq{ctx: ctx})
		r.serveLeaseRead(!noLease)
	case r.lead != 0:
		m := core.MessageValue{Kind: core.MsgReadIndex, To: r.lead, Context: ctx}
		if noLease {
			m.Flags |= core.FlagNoLease
		}
		r.send(m)
	default:
		r.rd.ReadStates = append(r.rd.ReadStates, ReadState{Context: ctx, Code: codeNotLeader})
	}
}

// serveLeaseRead answers every pending read at the commit index at once,
// with no round, while the lease holds and the caller allows it; otherwise
// the reads wait for flushReads.
func (r *Raft) serveLeaseRead(allowLease bool) {
	if !allowLease || !r.LeaseValid(r.now) {
		return
	}
	for _, q := range r.pendingReads {
		r.answerRead(q, r.log.committed, 0)
	}
	r.pendingReads = r.pendingReads[:0]
}

// flushReads starts ONE round for every read that arrived since the last
// one: a read never rides a round already in flight (D9).
func (r *Raft) flushReads() {
	if r.role != core.RoleLeader || len(r.pendingReads) == 0 || r.log.committed < r.termStart {
		return
	}
	if r.mutated(mutReadNoRound) {
		for _, q := range r.pendingReads {
			r.answerRead(q, r.log.committed, 0)
		}
		r.pendingReads = r.pendingReads[:0]
		return
	}
	r.broadcastHeartbeat(r.now)
	id := r.nextRound
	for _, q := range r.pendingReads {
		q.round, q.index = id, r.log.committed
		r.waitingReads = append(r.waitingReads, q)
	}
	r.pendingReads = r.pendingReads[:0]
	r.confirmRounds()
}

// contractWindow is the minimum election timeout of the contract a voter
// applied, the window it refuses votes in; an unknown generation grants no
// lease. It used to count as the shortest contract known: a leader started
// from a snapshot cut after a contract change knows only the newer one,
// and sized the acknowledgements of a follower still on the older, shorter
// contract at the newer window, longer than that follower refused votes
// for (#522).
func (r *Raft) contractWindow(gen uint64) time.Duration {
	if c, ok := r.contracts[gen]; ok {
		return c.ElectionTimeoutMin
	}
	if r.mutated(mutUnknownGenShortestKnown) {
		w := r.timing.ElectionTimeoutMin
		for _, c := range r.contracts {
			w = min(w, c.ElectionTimeoutMin)
		}
		return w
	}
	return 0
}

// confirmRounds settles the latest round a quorum of the current
// configuration acknowledged: it extends the lease from that round's send
// instant, answers every read waiting on it or an earlier one, and forgets
// those rounds.
func (r *Raft) confirmRounds() {
	confirmed, sentAt, leaseLen := r.lastConfirmedRound()
	if confirmed == 0 {
		return
	}
	r.extendLease(sentAt, leaseLen)
	r.answerConfirmed(confirmed)
	r.dropRoundsThrough(confirmed)
}

// lastConfirmedRound returns the latest round of the current configuration
// a quorum acknowledged, its send instant and the lease it grants; a zero
// id when there is none. A round of an older configuration never counts
// (R2-15).
func (r *Raft) lastConfirmedRound() (uint64, time.Time, time.Duration) {
	confIdx := r.conf().Index
	confirmed := uint64(0)
	var sentAt time.Time
	var leaseLen time.Duration
	for _, rd := range r.rounds {
		if rd.conf != confIdx {
			continue
		}
		n, minWindow := r.roundAcks(rd)
		if n >= r.quorum() {
			confirmed, sentAt, leaseLen = rd.id, rd.sentAt, minWindow
		}
	}
	return confirmed, sentAt, leaseLen
}

// roundAcks counts the voters that acknowledged rd and returns the shortest
// election window among the contracts they applied: the lease a round may
// grant is the one every acknowledging voter honours (D9 d).
func (r *Raft) roundAcks(rd *round) (int, time.Duration) {
	n := 0
	minWindow := time.Duration(0)
	for _, id := range r.voters() {
		if g := rd.acks[id]; g != 0 {
			if w := r.contractWindow(g - 1); n == 0 || w < minWindow {
				minWindow = w
			}
			n++
		}
	}
	return n, minWindow
}

// extendLease moves the lease start to a confirmed round sent later than
// the current one, shortened by the drift budget; a dropped lease stays
// dropped for the term.
func (r *Raft) extendLease(sentAt time.Time, leaseLen time.Duration) {
	if r.facts&raftLeaseDropped != 0 || !sentAt.After(r.leaseStart) {
		return
	}
	r.leaseStart = sentAt
	if r.mutated(mutLeaseLeaderView) {
		leaseLen = r.timing.ElectionTimeoutMin
	}
	r.leaseLen = time.Duration(float64(leaseLen) * (1 - r.timing.MaxClockDrift))
}

// answerConfirmed answers, in arrival order, every read waiting on a round
// through confirmed, and keeps the others waiting.
func (r *Raft) answerConfirmed(confirmed uint64) {
	k := 0
	for _, q := range r.waitingReads {
		if q.round <= confirmed {
			r.answerRead(q, q.index, 0)
			continue
		}
		r.waitingReads[k] = q
		k++
	}
	r.waitingReads = r.waitingReads[:k]
}

// dropRoundsThrough forgets every round through confirmed, keeping the
// slice's storage.
func (r *Raft) dropRoundsThrough(confirmed uint64) {
	n := 0
	for n < len(r.rounds) && r.rounds[n].id <= confirmed {
		n++
	}
	r.rounds = append(r.rounds[:0], r.rounds[n:]...)
}

// answerRead answers a read at index, or refuses it with code: locally in
// Ready.ReadStates, or with a message to the follower that forwarded it.
func (r *Raft) answerRead(q readReq, index uint64, code errs.Code) {
	if q.from == 0 || q.from == r.id {
		r.rd.ReadStates = append(r.rd.ReadStates, ReadState{Context: q.ctx, Index: index, Code: code})
		return
	}
	r.send(core.MessageValue{Kind: core.MsgReadIndexResp, To: q.from, Context: q.ctx, Index: index, Code: code})
}

// ---- leadership transfer ----

// TransferLeadership starts a transfer to a voter (D8). It refuses new
// proposals from now on and drops the lease for the rest of the term (D9 c).
func (r *Raft) TransferLeadership(now time.Time, to core.NodeID) errs.Code {
	r.now = now
	if r.role != core.RoleLeader {
		return codeNotLeader
	}
	if to == r.id || !r.isVoter(to) {
		return codeMembershipInvalid
	}
	r.startTransfer(to)
	return 0
}

// startTransfer starts a transfer to to unless one runs: from now on the
// leader proposes nothing and holds no lease, catches the target up, and
// sends TimeoutNow once it holds the whole log.
func (r *Raft) startTransfer(to core.NodeID) {
	if r.transferTo != 0 {
		return
	}
	r.transferTo = to
	r.facts &^= raftTransferSent
	r.transferDeadline = r.now.Add(r.timing.ElectionTimeoutMax)
	if !r.mutated(mutLeaseAfterTransfer) {
		r.facts |= raftLeaseDropped
	}
	if pr := r.prs[to]; pr != nil && pr.match < r.log.lastIndex() {
		r.sendAppend(to, false)
	}
	r.maybeSendTimeoutNow()
}

// maybeSendTimeoutNow sends TimeoutNow only once every proposed entry is
// committed and the target holds them all (D8, from raft-sql-poc).
func (r *Raft) maybeSendTimeoutNow() {
	if r.transferTo == 0 || r.facts&raftTransferSent != 0 || r.role != core.RoleLeader {
		return
	}
	pr := r.prs[r.transferTo]
	last := r.log.lastIndex()
	if pr == nil || r.log.committed != last || pr.match != last {
		return
	}
	r.facts |= raftTransferSent
	r.send(core.MessageValue{Kind: core.MsgTimeoutNow, To: r.transferTo})
}

// ---- snapshots ----

// CheckOffer answers an install offer on the event loop, which alone knows
// the term, the leader and the commit index (R8-21). needTable exempts a
// node that holds no usable snapshot from the freshness rule (R7-18).
func (r *Raft) CheckOffer(from core.NodeID, o core.InstallOfferValue, needTable bool) errs.Code {
	if o.Term != r.term || from != r.lead || r.role == core.RoleLeader {
		return codeNotLeader
	}
	if o.Meta.ClusterVersion > r.speaks() {
		// A raise past what this node speaks: the driver stops the node on
		// this refusal, held or not (D18, #640).
		return codeVersion
	}
	if o.Meta.Position.Index <= r.log.committed && !needTable {
		return codePrecondition
	}
	if o.Meta.Position.Index < r.log.snapIndex && !r.mutated(mutInstallBelowBase) {
		// Even with no table: a snapshot below the log's base would drop
		// durable entries this node acknowledged, committed ones included.
		// The leader cuts a newer one.
		return core.CodeCompacted
	}
	return 0
}

// InstallSnapshot moves the log onto a snapshot the driver has published and
// restored. The suffix after it is kept only when the boundary entry matches
// (D7).
func (r *Raft) InstallSnapshot(meta core.SnapshotMetaValue) {
	idx, t := meta.Position.Index, meta.Position.Term
	// A snapshot above the protocol this node speaks installs nothing: the
	// node stops on the Ready (#640).
	if r.refusesClusterVersion(&meta) || r.ignoresInstall(idx) {
		return
	}
	if r.log.matchTerm(idx, t) && idx <= r.log.lastIndex() {
		r.log.compactTo(idx, t, meta.ChainTip)
		r.dropConfsThrough(idx)
		r.rd.Compact = max(r.rd.Compact, idx)
	} else {
		r.restartLogAt(idx, t, meta.ChainTip)
	}
	r.log.committed = max(r.log.committed, idx)
	r.log.applied = max(r.log.applied, idx)
	r.log.stable = max(r.log.stable, idx)
	r.base = meta.Membership.Clone()
	r.base.Index = idx
	r.installEngine(&meta)
	r.cfg.NoTable = false
	r.confChanged()
	// A durable snapshot through the commit that met the silence condition
	// ends the silence as the entries would (#684).
	r.endSilenceOnceDurable()
	// The install proves the prefix up to idx only: a kept suffix may be
	// another term's, and the next append verifies it (review H1).
	ack := idx
	if r.mutated(mutSnapshotAckLastIndex) {
		ack = r.log.lastIndex()
	}
	if r.lead != 0 {
		r.send(core.MessageValue{Kind: core.MsgAppResp, To: r.lead, Index: ack})
	}
}

// ignoresInstall reports whether an install at idx must leave the log as it
// is. One below the base never moves it back: CheckOffer refuses it.
//
// The offer was checked fresh before its body streamed and the machine
// restored it, which takes seconds on a large state. Meanwhile this node
// may have been elected, or committed past the snapshot through a new
// leader's appends: its log and its engine state then hold everything the
// snapshot holds, and the effects of the engine entries committed past it
// besides. Moving onto the snapshot would roll that engine state back, and
// a later cut would carry the regression to the other nodes (#416). A node
// with no table still installs (installEngine replays those effects).
func (r *Raft) ignoresInstall(idx uint64) bool {
	if idx < r.log.snapIndex && !r.mutated(mutInstallBelowBase) {
		return true
	}
	return r.role == core.RoleLeader || (idx <= r.log.committed && !r.cfg.NoTable)
}

// installEngine replaces the engine's own replicated state with the
// snapshot's, then applies again the effects of the entries this node holds
// committed past it, which only a node with no table can (#416).
func (r *Raft) installEngine(meta *core.SnapshotMetaValue) {
	if adoptsTiming(meta.Timing, r.timing) {
		if r.ownTiming != 0 && !r.mutated(mutOwnTimingKept) {
			// This node's own InitialTiming named a generation the cluster
			// never confirmed to it: an acknowledgement of that generation
			// is sized by nothing, rather than by this node's guess (#522).
			delete(r.contracts, r.ownTiming)
			r.ownTiming = 0
		}
		r.timing = meta.Timing
		r.contracts[r.timing.Generation] = r.timing
	}
	if meta.Timing.ElectionTimeoutMin > r.voteWindow {
		r.voteWindow = meta.Timing.ElectionTimeoutMin
	}
	r.clusterVer, r.clusterGen, r.noSpace = meta.ClusterVersion, meta.ClusterGeneration, meta.NoSpace
	r.resetEngines()
	r.applyCommitEffects(meta.Position.Index+1, r.log.committed)
}

// restartLogAt discards the whole log, whose boundary entry does not match
// the snapshot at (idx, t), and restarts it empty on that snapshot: the
// entries dropped are truncated in the Ready and the store compacted to idx.
func (r *Raft) restartLogAt(idx, t uint64, chainTip uint32) {
	if r.log.lastIndex() > r.log.snapIndex {
		r.truncateFrom(r.log.snapIndex + 1)
	}
	r.log.ents = nil
	r.log.window, r.log.evicted = 0, idx
	r.log.snapIndex, r.log.snapTerm, r.log.snapChain = idx, t, chainTip
	r.log.stable = max(r.log.stable, idx)
	r.confs = nil
	r.rd.Compact = max(r.rd.Compact, idx)
}

// dropConfsThrough forgets every configuration appended at or before index,
// which the snapshot base now carries, keeping the slice's storage.
func (r *Raft) dropConfsThrough(index uint64) {
	n := 0
	for n < len(r.confs) && r.confs[n].index <= index {
		n++
	}
	r.confs = append(r.confs[:0], r.confs[n:]...)
}

// ownCopy returns a copy of b the step function owns, out of a frame the
// transport reuses. An empty b yields nil, exactly as the append([]byte(nil),
// b...) it replaces did, so a copied entry compares equal to its original.
func ownCopy(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return slices.Clone(b)
}
