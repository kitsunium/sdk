package raft

import (
	"slices"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

const (
	codeNotLeader         = core.CodeNotLeader
	codeChangePending     = core.CodeMembershipChangePending
	codeMembershipInvalid = core.CodeMembershipInvalid
	codeRetired           = core.CodeNodeIDRetired
	codePrecondition      = core.CodeAdminPreconditionFailed
	codeTransferFailed    = core.CodeTransferFailed
	codeVersion           = core.CodeVersionUnsupported
	codeMisconfigured     = core.CodeConsensusMisconfigured
)

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
		pr.heard = true
	}
	r.leaseDropped, r.leaseStart = false, time.Time{}
	r.rounds, r.pendingReads, r.waitingReads = nil, nil, nil
	r.transferTo = 0
	// A new leader commits a no-op of its term before it serves a read or
	// accepts a membership change (D6).
	noop := core.Entry{Kind: core.EntryNoOp, BodySum: wire.BodySum(nil)}
	r.appendAsLeader([]core.Entry{noop})
	r.termStart = r.log.lastIndex()
	r.heartbeatDue = r.now
	r.broadcastAppend(false)
}

// appendAsLeader assigns positions and chain values at the leader's
// serialized append point (D4) and appends.
func (r *Raft) appendAsLeader(es []core.Entry) []core.Position {
	pos := make([]core.Position, len(es))
	chain := r.log.chain(r.log.lastIndex())
	idx := r.log.lastIndex()
	for i := range es {
		idx++
		es[i].Position = core.Position{Term: r.term, Index: idx}
		es[i].ChainSum = wire.ChainSum(chain, &es[i])
		chain = es[i].ChainSum
		pos[i] = es[i].Position
	}
	r.appendEntries(es)
	return pos
}

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
		r.send(core.Message{Kind: core.MsgHeartbeat, To: id, Commit: commit, Context: rd.id})
		if pr.state == stateReplicate && pr.paused(r.cfg.MaxInflight, r.cfg.MaxInflightBytes) {
			// A full window must not stall forever on a lost
			// acknowledgement: an empty append at next (R2-07).
			if t, ok := r.log.term(pr.next - 1); ok {
				r.send(core.Message{Kind: core.MsgApp, To: id, Index: pr.next - 1, LogTerm: t, Commit: min(pr.match, r.log.committed)})
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

func (r *Raft) broadcastAppend(sendIfEmpty bool) {
	for _, id := range r.peers() {
		r.sendAppend(id, sendIfEmpty)
	}
}

func (r *Raft) sendAppend(to core.NodeID, sendIfEmpty bool) {
	pr := r.prs[to]
	if pr == nil || pr.paused(r.cfg.MaxInflight, r.cfg.MaxInflightBytes) {
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
		// The payloads were evicted: the driver reads them off the loop.
		pr.fetching = true
		r.rd.Fetch = append(r.rd.Fetch, FetchRequest{To: to, Lo: pr.next, Hi: min(r.log.evicted, r.log.lastIndex()) + 1})
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
func (r *Raft) Fetched(to core.NodeID, lo uint64, ents []core.Entry, compacted bool) {
	pr := r.prs[to]
	if pr == nil || !pr.fetching {
		return
	}
	pr.fetching = false
	if r.role != core.RoleLeader || pr.next != lo || pr.state == stateSnapshot {
		r.sendAppend(to, false)
		return
	}
	if compacted {
		pr.becomeSnapshot(r.log.snapIndex)
		r.rd.SendSnapshot = append(r.rd.SendSnapshot, to)
		return
	}
	for i := range ents {
		if t, ok := r.log.term(ents[i].Position.Index); !ok || t != ents[i].Position.Term || ents[i].Position.Index != lo+uint64(i) {
			r.sendAppend(to, false)
			return
		}
	}
	prevTerm, ok := r.log.term(lo - 1)
	if !ok || len(ents) == 0 {
		r.sendAppend(to, false)
		return
	}
	r.sendEntries(pr, to, lo-1, prevTerm, ents)
}

func (r *Raft) sendEntries(pr *progress, to core.NodeID, prev, prevTerm uint64, ents []core.Entry) {
	m := core.Message{Kind: core.MsgApp, To: to, Index: prev, LogTerm: prevTerm, Entries: ents, Commit: r.log.committed}
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
		pr.probeSent = true
	}
}

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

func (r *Raft) stepLeader(now time.Time, m core.Message) {
	pr := r.prs[m.From]
	switch m.Kind {
	case core.MsgReadIndex:
		r.pendingReads = append(r.pendingReads, readReq{ctx: m.Context, from: m.From})
		r.serveLeaseRead(m.Flags&core.FlagNoLease == 0)
		return
	case core.MsgVoteResp, core.MsgPreVoteResp:
		return
	}
	if pr == nil {
		return
	}
	pr.lastHeard = now
	pr.heard = true
	switch m.Kind {
	case core.MsgAppResp:
		r.handleAppResp(pr, m)
	case core.MsgHeartbeatResp:
		for _, rd := range r.rounds {
			if rd.id <= m.Context {
				rd.acks[m.From] = m.Contract + 1
			}
		}
		r.confirmRounds()
		if pr.state == stateProbe {
			pr.probeSent = false
		}
		if pr.state == stateReplicate && len(pr.inflight) > 0 && pr.match == pr.hbMatch {
			// An append in the window was lost: the transport may drop,
			// and a window that never refills must not stall (R1-11).
			pr.becomeProbe()
		}
		pr.hbMatch = pr.match
		if pr.match < r.log.lastIndex() {
			r.sendAppend(m.From, false)
		}
	}
}

func (r *Raft) handleAppResp(pr *progress, m core.Message) {
	if m.Reject {
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
		switch pr.state {
		case stateProbe:
			if m.Index != pr.next-1 {
				return
			}
		case stateReplicate:
			if m.Index <= pr.match || m.Index >= pr.next {
				return
			}
		default:
			return
		}
		pr.next = max(min(m.Index, m.RejectHint+1), pr.match+1)
		if pr.state == stateReplicate {
			pr.state = stateProbe
			pr.freeAll()
		}
		pr.probeSent = false
		r.sendAppend(m.From, false)
		if !pr.probeSent {
			r.sendAppend(m.From, true)
		}
		return
	}
	if pr.maybeUpdate(m.Index) {
		switch {
		case pr.state == stateProbe:
			pr.becomeReplicate()
		case pr.state == stateSnapshot && pr.match >= pr.pendingSnapshot:
			pr.becomeProbe()
			pr.becomeReplicate()
		}
		pr.freeTo(m.Index)
		if r.maybeCommit() {
			r.broadcastAppend(true)
		}
		r.maybeSendTimeoutNow()
	} else {
		pr.freeTo(m.Index)
	}
	if pr.match < r.log.lastIndex() {
		r.sendAppend(m.From, false)
	}
}

// ---- proposals ----

// Propose appends entries at the leader. Kind, Version, BodySum and Data are
// the proposer's; positions and chain values are assigned here. A refusal
// appends nothing.
func (r *Raft) Propose(now time.Time, es []core.Entry) ([]core.Position, errs.Code) {
	r.now = now
	if r.role != core.RoleLeader || r.transferTo != 0 {
		return nil, codeNotLeader
	}
	var transfer core.NodeID
	for i := range es {
		e := &es[i]
		switch {
		case e.Kind == core.EntryCommand:
		case e.Kind == core.EntryNoOp || !e.Kind.Known():
			return nil, codeVersion
		default:
			to, code := r.validateAdmin(e, r.log.lastIndex()+uint64(i)+1)
			if code != 0 {
				return nil, code
			}
			if to != 0 {
				transfer = to
			}
		}
	}
	pos := r.appendAsLeader(es)
	r.broadcastAppend(false)
	if transfer != 0 {
		r.startTransfer(transfer)
	}
	return pos, 0
}

// validateAdmin re-validates an engine entry against the leader's own state
// and rewrites a membership change into the whole configuration (D8).
func (r *Raft) validateAdmin(e *core.Entry, index uint64) (core.NodeID, errs.Code) {
	a, err := wire.ParseAdmin(e.Data)
	if err != nil {
		return 0, codeVersion
	}
	var transfer core.NodeID
	switch e.Kind {
	case core.EntryMembership:
		if !r.mutated(mutMembershipBeforeNoOp) && r.log.committed < r.termStart {
			return 0, codeChangePending
		}
		if r.conf().Index > r.log.committed {
			return 0, codeChangePending
		}
		next, code := r.nextConf(a)
		if code != 0 {
			return 0, code
		}
		next.Index = index
		a.Membership = next
	case core.EntryTimingContract:
		if !a.Pre.Log.Same(r.cfg.Log) || a.Pre.Generation != r.timing.Generation {
			return 0, codePrecondition
		}
		c := a.Contract
		if c.HeartbeatInterval <= 0 || c.ElectionTimeoutMin <= 0 || c.HeartbeatInterval*5 > c.ElectionTimeoutMin ||
			c.ElectionTimeoutMax <= c.ElectionTimeoutMin || c.MaxClockDrift < 0 || c.MaxClockDrift >= 1 {
			return 0, codeMisconfigured
		}
		// From the moment a new contract is proposed, the lease is dropped
		// (D9 d).
		r.leaseDropped = true
	case core.EntryClusterVersion:
		if !a.Pre.Log.Same(r.cfg.Log) || a.Pre.Generation != r.clusterGen || a.ClusterVersion <= r.clusterVer {
			return 0, codePrecondition
		}
	case core.EntryNoSpaceClear:
		if !a.Pre.Log.Same(r.cfg.Log) || r.noSpace == 0 || a.Pre.Alarm != r.noSpace {
			return 0, codePrecondition
		}
	case core.EntryNoSpaceRaise:
	case core.EntryTransfer:
		if a.Target == r.id || !r.isVoter(a.Target) {
			return 0, codeMembershipInvalid
		}
		transfer = a.Target
	}
	e.Data = wire.AppendAdmin(nil, &a)
	e.BodySum = wire.BodySum(e.Data)
	return transfer, 0
}

// nextConf computes the configuration a single-server change produces, and
// refuses it on its merits (D8).
func (r *Raft) nextConf(a wire.Admin) (core.Membership, errs.Code) {
	cur := r.conf().Clone()
	idx := slices.IndexFunc(cur.Members, func(m core.Member) bool { return m.ID == a.Target })
	switch a.Change {
	case wire.ChangeAddLearner:
		if len(a.Membership.Members) != 1 {
			return cur, codeMembershipInvalid
		}
		nm := a.Membership.Members[0]
		if cur.IsRetired(nm.ID) {
			return cur, codeRetired
		}
		if nm.ID == 0 || nm.SPKI == ([32]byte{}) {
			return cur, codeMembershipInvalid
		}
		if _, ok := cur.Member(nm.ID); ok {
			return cur, codeMembershipInvalid
		}
		if !r.quorumActive(r.now, r.voteWindow) {
			return cur, codeMembershipInvalid
		}
		nm.Voter = false
		cur.Members = append(cur.Members, nm)
	case wire.ChangePromote:
		if cur.IsRetired(a.Target) {
			return cur, codeRetired
		}
		if idx < 0 || cur.Members[idx].Voter {
			return cur, codeMembershipInvalid
		}
		pr := r.prs[a.Target]
		// Recent DURABLE progress within PromoteLag of the commit, heard
		// within the last election timeout (D8).
		if pr == nil || !pr.heard || pr.match+r.cfg.PromoteLag < r.log.committed ||
			r.now.Sub(pr.lastHeard) >= r.timing.ElectionTimeoutMax {
			return cur, codeMembershipInvalid
		}
		cur.Members[idx].Voter = true
		cur.Members[idx].Joining = false
		if !r.activeMajority(cur) {
			return cur, codeMembershipInvalid
		}
	case wire.ChangeRemove:
		if idx < 0 || a.Target == r.id {
			return cur, codeMembershipInvalid
		}
		wasVoter := cur.Members[idx].Voter
		cur.Members = slices.Delete(cur.Members, idx, idx+1)
		cur.Retired = append(cur.Retired, a.Target)
		if wasVoter {
			n := len(cur.Voters())
			if n == 0 || n < r.cfg.MinVoters {
				return cur, codeMembershipInvalid
			}
		}
		if !r.activeMajority(cur) {
			return cur, codeMembershipInvalid
		}
	case wire.ChangeUpdateAddress:
		if idx < 0 || len(a.Membership.Members) != 1 {
			return cur, codeMembershipInvalid
		}
		if !r.activeMajority(cur) {
			return cur, codeMembershipInvalid
		}
		cur.Members[idx].Address = a.Membership.Members[0].Address
	case wire.ChangeDenyPin:
		if idx < 0 {
			return cur, codeMembershipInvalid
		}
		cur.Members[idx].SPKI = [32]byte{}
	default:
		return cur, codeMembershipInvalid
	}
	return cur, 0
}

// activeMajority is etcd's strict reconfiguration check: the voters of m
// that answer the leader NOW form a majority of m (R2-27).
func (r *Raft) activeMajority(m core.Membership) bool {
	vs := m.Voters()
	n := 0
	for _, id := range vs {
		if id == r.id {
			n++
			continue
		}
		if pr := r.prs[id]; pr != nil && pr.heard && r.now.Sub(pr.lastHeard) < r.voteWindow {
			n++
		}
	}
	return len(vs) > 0 && n >= len(vs)/2+1
}

func (r *Raft) handleForward(m core.Message) {
	resp := core.MsgForwardResp
	if m.Kind == core.MsgForwardAdmin {
		resp = core.MsgForwardAdminResp
	}
	if r.role != core.RoleLeader || len(m.Entries) != 1 {
		r.send(core.Message{Kind: resp, To: m.From, Context: m.Context, Reject: true, Code: codeNotLeader, RejectHint: uint64(r.lead)})
		return
	}
	e := m.Entries[0]
	e.Data = append([]byte(nil), e.Data...)
	if wire.BodySum(e.Data) != e.BodySum {
		r.send(core.Message{Kind: resp, To: m.From, Context: m.Context, Reject: true, Code: core.CodeFrameInvalid})
		return
	}
	if (m.Kind == core.MsgForward) != (e.Kind == core.EntryCommand) {
		r.send(core.Message{Kind: resp, To: m.From, Context: m.Context, Reject: true, Code: codeVersion})
		return
	}
	pos, code := r.Propose(r.now, []core.Entry{e})
	if code != 0 {
		r.send(core.Message{Kind: resp, To: m.From, Context: m.Context, Reject: true, Code: code, RejectHint: uint64(r.lead)})
		return
	}
	r.send(core.Message{Kind: resp, To: m.From, Context: m.Context, Index: pos[0].Index, LogTerm: pos[0].Term})
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
		m := core.Message{Kind: core.MsgReadIndex, To: r.lead, Context: ctx}
		if noLease {
			m.Flags |= core.FlagNoLease
		}
		r.send(m)
	default:
		r.rd.ReadStates = append(r.rd.ReadStates, ReadState{Context: ctx, Code: codeNotLeader})
	}
}

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
// applied; an unknown generation counts as the shortest contract known.
func (r *Raft) contractWindow(gen uint64) time.Duration {
	if c, ok := r.contracts[gen]; ok {
		return c.ElectionTimeoutMin
	}
	w := r.timing.ElectionTimeoutMin
	for _, c := range r.contracts {
		w = min(w, c.ElectionTimeoutMin)
	}
	return w
}

func (r *Raft) confirmRounds() {
	confIdx := r.conf().Index
	confirmed := uint64(0)
	var sentAt time.Time
	var leaseLen time.Duration
	for _, rd := range r.rounds {
		if rd.conf != confIdx {
			continue
		}
		n := 0
		minWindow := time.Duration(0)
		for _, id := range r.voters() {
			if g := rd.acks[id]; g != 0 {
				n++
				if w := r.contractWindow(g - 1); minWindow == 0 || w < minWindow {
					minWindow = w
				}
			}
		}
		if n >= r.quorum() {
			confirmed, sentAt, leaseLen = rd.id, rd.sentAt, minWindow
		}
	}
	if confirmed == 0 {
		return
	}
	if !r.leaseDropped && sentAt.After(r.leaseStart) {
		r.leaseStart = sentAt
		if r.mutated(mutLeaseLeaderView) {
			leaseLen = r.timing.ElectionTimeoutMin
		}
		r.leaseLen = time.Duration(float64(leaseLen) * (1 - r.timing.MaxClockDrift))
	}
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
	n := 0
	for n < len(r.rounds) && r.rounds[n].id <= confirmed {
		n++
	}
	r.rounds = append(r.rounds[:0], r.rounds[n:]...)
}

func (r *Raft) answerRead(q readReq, index uint64, code errs.Code) {
	if q.from == 0 || q.from == r.id {
		r.rd.ReadStates = append(r.rd.ReadStates, ReadState{Context: q.ctx, Index: index, Code: code})
		return
	}
	r.send(core.Message{Kind: core.MsgReadIndexResp, To: q.from, Context: q.ctx, Index: index, Code: code})
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

func (r *Raft) startTransfer(to core.NodeID) {
	if r.transferTo != 0 {
		return
	}
	r.transferTo = to
	r.transferSent = false
	r.transferDeadline = r.now.Add(r.timing.ElectionTimeoutMax)
	if !r.mutated(mutLeaseAfterTransfer) {
		r.leaseDropped = true
	}
	if pr := r.prs[to]; pr != nil && pr.match < r.log.lastIndex() {
		r.sendAppend(to, false)
	}
	r.maybeSendTimeoutNow()
}

// maybeSendTimeoutNow sends TimeoutNow only once every proposed entry is
// committed and the target holds them all (D8, from raft-sql-poc).
func (r *Raft) maybeSendTimeoutNow() {
	if r.transferTo == 0 || r.transferSent || r.role != core.RoleLeader {
		return
	}
	pr := r.prs[r.transferTo]
	last := r.log.lastIndex()
	if pr == nil || r.log.committed != last || pr.match != last {
		return
	}
	r.transferSent = true
	r.send(core.Message{Kind: core.MsgTimeoutNow, To: r.transferTo})
}

// ---- snapshots ----

// CheckOffer answers an install offer on the event loop, which alone knows
// the term, the leader and the commit index (R8-21). needTable exempts a
// node that holds no usable snapshot from the freshness rule (R7-18).
func (r *Raft) CheckOffer(from core.NodeID, o core.InstallOffer, needTable bool) errs.Code {
	if o.Term != r.term || from != r.lead || r.role == core.RoleLeader {
		return codeNotLeader
	}
	if o.Meta.Position.Index <= r.log.committed && !needTable {
		return codePrecondition
	}
	return 0
}

// InstallSnapshot moves the log onto a snapshot the driver has published and
// restored. The suffix after it is kept only when the boundary entry matches
// (D7).
func (r *Raft) InstallSnapshot(meta core.SnapshotMeta) {
	idx, t := meta.Position.Index, meta.Position.Term
	if r.log.matchTerm(idx, t) && idx <= r.log.lastIndex() {
		r.log.compactTo(idx, t, meta.ChainTip)
		n := 0
		for n < len(r.confs) && r.confs[n].index <= idx {
			n++
		}
		r.confs = append(r.confs[:0], r.confs[n:]...)
		r.rd.Compact = max(r.rd.Compact, idx)
	} else {
		if r.log.lastIndex() > r.log.snapIndex {
			r.truncateFrom(r.log.snapIndex + 1)
		}
		r.log.ents = nil
		r.log.window, r.log.evicted = 0, idx
		r.log.snapIndex, r.log.snapTerm, r.log.snapChain = idx, t, meta.ChainTip
		r.log.stable = max(r.log.stable, idx)
		r.confs = nil
		r.rd.Compact = max(r.rd.Compact, idx)
	}
	r.log.committed = max(r.log.committed, idx)
	r.log.applied = max(r.log.applied, idx)
	r.log.stable = max(r.log.stable, idx)
	r.base = meta.Membership.Clone()
	r.base.Index = idx
	if meta.Timing.Generation > r.timing.Generation {
		r.timing = meta.Timing
	}
	if meta.Timing.ElectionTimeoutMin > r.voteWindow {
		r.voteWindow = meta.Timing.ElectionTimeoutMin
	}
	r.clusterVer, r.clusterGen, r.noSpace = meta.ClusterVersion, meta.ClusterGeneration, meta.NoSpace
	r.cfg.NoTable = false
	r.confChanged()
	if r.lead != 0 {
		r.send(core.Message{Kind: core.MsgAppResp, To: r.lead, Index: r.log.lastIndex()})
	}
}
