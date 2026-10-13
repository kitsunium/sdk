package raft

import (
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// Step handles one message. The driver has already checked that m.From is
// the peer the transport authenticated (R7-05).
func (r *Raft) Step(now time.Time, m core.Message) {
	r.now = now
	if m.Group != r.cfg.Group {
		return
	}
	switch {
	case m.Term == 0:
		// local-only kinds carry no term
	case m.Term > r.term:
		if m.Kind == core.MsgTimeoutNow && !r.mutated(mutTimeoutNowAnyTerm) {
			// TimeoutNow is honoured only at the receiver's own term (R2-13).
			return
		}
		if m.Kind == core.MsgPreVote || m.Kind == core.MsgVote {
			if r.refuseForLiveLeader(now, m) {
				// A refusal never moves a term (R1-18).
				return
			}
		}
		switch {
		case m.Kind == core.MsgPreVote:
			// A PreVote never raises anybody's term.
		case m.Kind == core.MsgPreVoteResp && !m.Reject:
			// The future term a granted PreVote answers.
		case m.Kind == core.MsgApp || m.Kind == core.MsgHeartbeat:
			r.becomeFollower(m.Term, m.From)
		default:
			r.becomeFollower(m.Term, 0)
		}
	case m.Term < r.term:
		switch m.Kind {
		case core.MsgApp, core.MsgHeartbeat:
			// A leader of an older term learns of ours, so that it steps
			// down and a PreVote round decides (R1-18).
			r.send(core.Message{Kind: core.MsgAppResp, To: m.From, Reject: true, Index: m.Index})
		case core.MsgPreVote:
			r.send(core.Message{Kind: core.MsgPreVoteResp, To: m.From, Term: r.term, Reject: true})
		}
		return
	}

	switch m.Kind {
	case core.MsgPreVote, core.MsgVote:
		r.handleVote(now, m)
		return
	case core.MsgForward, core.MsgForwardAdmin:
		r.handleForward(m)
		return
	case core.MsgForwardResp, core.MsgForwardAdminResp, core.MsgReadIndexResp:
		if m.Kind == core.MsgReadIndexResp {
			r.rd.ReadStates = append(r.rd.ReadStates, ReadState{Context: m.Context, Index: m.Index, Code: m.Code})
			return
		}
		r.rd.Forwarded = append(r.rd.Forwarded, m)
		return
	}

	switch r.role {
	case core.RoleLeader:
		r.stepLeader(now, m)
	case core.RoleCandidate, core.RolePreCandidate:
		r.stepCandidate(now, m)
	default:
		r.stepFollower(now, m)
	}
}

// refuseForLiveLeader is D5's vote-side CheckQuorum: a node that heard from
// a live leader within the window, or that just started, refuses a vote of
// a higher term unless the request carries a valid transfer flag. A leader
// refuses too while a majority answers it (R7-19).
func (r *Raft) refuseForLiveLeader(now time.Time, m core.Message) bool {
	if r.mutated(mutLiveLeaderPreVoteOnly) && m.Kind == core.MsgVote {
		return false
	}
	if r.validTransferFlag(m) {
		return false
	}
	if r.role == core.RoleLeader {
		return r.quorumActive(now, r.voteWindow)
	}
	return !r.lastLeaderContact.IsZero() && now.Sub(r.lastLeaderContact) < r.voteWindow
}

// validTransferFlag honours a transfer flag only for one campaign: issued by
// the leader this node knows for the flag's term, for the very next term
// (R7-17, R8-37).
func (r *Raft) validTransferFlag(m core.Message) bool {
	if m.Flags&core.FlagTransfer == 0 {
		return false
	}
	if r.mutated(mutTransferAnyTerm) {
		return true
	}
	if m.TransferTerm != r.term || m.Term != m.TransferTerm+1 {
		return false
	}
	if r.role == core.RoleLeader {
		return m.TransferIssuer == r.id
	}
	return m.TransferIssuer == r.lead && r.lead != 0
}

func (r *Raft) upToDate(m core.Message) bool {
	lt := r.log.lastTerm()
	return m.LogTerm > lt || (m.LogTerm == lt && m.Index >= r.log.lastIndex())
}

func (r *Raft) handleVote(now time.Time, m core.Message) {
	pre := m.Kind == core.MsgPreVote
	resp := core.MsgVoteResp
	if pre {
		resp = core.MsgPreVoteResp
	}
	canVote := r.vote == m.From ||
		(r.vote == 0 && r.lead == 0) ||
		(pre && m.Term > r.term)
	restricted := true
	switch {
	case pre && r.mutated(mutNoRestrictionPreVote):
		restricted = false
	case !pre && r.mutated(mutNoRestrictionVote):
		restricted = false
	case m.Flags&core.FlagTransfer != 0 && r.mutated(mutRestrictionUnderTransfer):
		restricted = false
	}
	ok := canVote && r.cfg.CanVote && r.cfg.SilentIndex == 0 && (!restricted || r.upToDate(m))
	if !ok {
		r.send(core.Message{Kind: resp, To: m.From, Term: r.term, Reject: true})
		return
	}
	if pre {
		// A PreVote's prospective term and vote are never persisted (R2-03).
		r.send(core.Message{Kind: resp, To: m.From, Term: m.Term})
		return
	}
	r.vote = m.From
	r.resetElection(now)
	r.send(core.Message{Kind: resp, To: m.From, Term: m.Term})
}

// ---- roles ----

func (r *Raft) becomeFollower(term uint64, lead core.NodeID) {
	if term != r.term {
		r.term = term
		r.vote = 0
	} else if r.mutated(mutSameTermVoteClear) {
		r.vote = 0
	}
	wasLeader := r.role == core.RoleLeader
	r.lead = lead
	r.role = core.RoleFollower
	if !r.isVoter(r.id) {
		r.role = core.RoleLearner
	}
	r.votes = map[core.NodeID]bool{}
	r.selfVoteSeq, r.selfVoteDurable = 0, false
	r.campaignIssuer, r.campaignTerm = 0, 0
	r.leaseDropped = false
	r.leaseStart = time.Time{}
	r.refusedLead = 0
	if wasLeader {
		r.abortLeadership()
	}
	r.resetElection(r.now)
}

// abortLeadership refuses what the old leader still held for its clients.
func (r *Raft) abortLeadership() {
	for _, q := range append(r.pendingReads, r.waitingReads...) {
		r.answerRead(q, 0, codeNotLeader)
	}
	r.pendingReads, r.waitingReads, r.rounds = nil, nil, nil
	if r.transferTo != 0 {
		res := TransferResult{To: r.transferTo}
		if !r.transferSent {
			res.Code = codeTransferFailed
		}
		r.rd.Transfers = append(r.rd.Transfers, res)
		r.transferTo = 0
	}
	r.prs = map[core.NodeID]*progress{}
}

func (r *Raft) campaign(pre bool, issuer core.NodeID, issuerTerm uint64) {
	if pre {
		r.role = core.RolePreCandidate
		r.lead = 0
		r.votes = map[core.NodeID]bool{r.id: true}
		r.resetElection(r.now)
		if r.tallyVotes() {
			return
		}
		for _, id := range r.voters() {
			if id == r.id {
				continue
			}
			r.send(core.Message{
				Kind: core.MsgPreVote, To: id, Term: r.term + 1,
				LogTerm: r.log.lastTerm(), Index: r.log.lastIndex(),
			})
		}
		return
	}
	r.term++
	r.vote = r.id
	r.lead = 0
	r.role = core.RoleCandidate
	r.votes = map[core.NodeID]bool{}
	r.selfVoteDurable = false
	r.campaignIssuer, r.campaignTerm = issuer, issuerTerm
	r.resetElection(r.now)
	for _, id := range r.voters() {
		if id == r.id {
			continue
		}
		m := core.Message{Kind: core.MsgVote, To: id, LogTerm: r.log.lastTerm(), Index: r.log.lastIndex()}
		if issuer != 0 {
			m.Flags |= core.FlagTransfer
			m.TransferIssuer, m.TransferTerm = issuer, issuerTerm
		}
		r.send(m)
	}
}

// tallyVotes moves a campaign on once a quorum granted; it reports whether
// the role changed.
func (r *Raft) tallyVotes() bool {
	granted, rejected := 0, 0
	for _, id := range r.voters() {
		v, ok := r.votes[id]
		switch {
		case !ok:
		case v:
			granted++
		default:
			rejected++
		}
	}
	q := r.quorum()
	switch {
	case granted >= q && r.role == core.RolePreCandidate:
		r.campaign(false, 0, 0)
		return true
	case granted >= q && r.role == core.RoleCandidate:
		r.becomeLeader()
		return true
	case rejected >= q:
		r.becomeFollower(r.term, 0)
		return true
	}
	return false
}

func (r *Raft) stepCandidate(now time.Time, m core.Message) {
	switch m.Kind {
	case core.MsgApp, core.MsgHeartbeat:
		r.becomeFollower(m.Term, m.From)
		r.stepFollower(now, m)
	case core.MsgPreVoteResp:
		if r.role == core.RolePreCandidate {
			r.votes[m.From] = !m.Reject
			r.tallyVotes()
		}
	case core.MsgVoteResp:
		if r.role == core.RoleCandidate {
			r.votes[m.From] = !m.Reject
			if r.selfVoteDurable {
				r.tallyVotes()
			}
		}
	case core.MsgReadIndex:
		r.send(core.Message{Kind: core.MsgReadIndexResp, To: m.From, Context: m.Context, Code: codeNotLeader})
	}
}

func (r *Raft) stepFollower(now time.Time, m core.Message) {
	switch m.Kind {
	case core.MsgApp:
		r.lead = m.From
		r.lastLeaderContact = now
		r.resetElection(now)
		r.handleAppend(m)
		r.maybeEndSilence(m)
	case core.MsgHeartbeat:
		r.lead = m.From
		r.lastLeaderContact = now
		r.resetElection(now)
		if r.refusedLead == m.From {
			// After N chain refusals, this leader's heartbeats are not
			// answered for the rest of the term (D4, R6-05).
			return
		}
		if c := min(m.Commit, r.log.lastIndex()); c > r.log.committed {
			r.commitTo(c)
		}
		r.send(core.Message{Kind: core.MsgHeartbeatResp, To: m.From, Context: m.Context, Contract: r.timing.Generation})
	case core.MsgTimeoutNow:
		// Accepted only from the leader of this term, at this term (R2-13).
		if !r.mutated(mutTimeoutNowAnyTerm) && (m.From != r.lead || m.Term != r.term) {
			return
		}
		if r.promotable() {
			r.campaign(false, m.From, m.Term)
		}
	case core.MsgReadIndex:
		r.send(core.Message{Kind: core.MsgReadIndexResp, To: m.From, Context: m.Context, Code: codeNotLeader})
	}
}

// handleAppend is the follower's side of AppendEntries, with the three rules
// raft-sql-poc learned as safety bugs (D6).
func (r *Raft) handleAppend(m core.Message) {
	if r.cfg.NoTable {
		// No usable snapshot: it asks for one instead of storing entries it
		// could never apply (D7, R7-18).
		r.send(core.Message{
			Kind: core.MsgAppResp, To: m.From, Index: m.Index, Reject: true,
			RejectHint: r.log.lastIndex(), Flags: core.FlagNeedSnapshot,
		})
		return
	}
	if m.Index < r.log.committed {
		r.send(core.Message{Kind: core.MsgAppResp, To: m.From, Index: r.log.committed})
		return
	}
	if !r.log.matchTerm(m.Index, m.LogTerm) {
		hint := min(m.Index, r.log.lastIndex())
		r.send(core.Message{Kind: core.MsgAppResp, To: m.From, Index: m.Index, Reject: true, RejectHint: hint})
		return
	}
	lastNew := m.Index + uint64(len(m.Entries))
	prevChain := r.log.chain(m.Index)
	start := -1
	for k := range m.Entries {
		e := &m.Entries[k]
		if e.Position.Index != m.Index+uint64(k)+1 {
			return
		}
		if t, ok := r.log.term(e.Position.Index); ok && t == e.Position.Term {
			prevChain = e.ChainSum
			continue
		}
		start = k
		break
	}
	if start >= 0 {
		// Verify the body and the chain before anything is appended: a
		// mismatch refuses the acknowledgement, and the leader resends (D4).
		chain := prevChain
		for k := start; k < len(m.Entries); k++ {
			e := &m.Entries[k]
			if !r.mutated(mutChainSkip) && (wire.BodySum(e.Data) != e.BodySum || wire.ChainSum(chain, e) != e.ChainSum) {
				r.chainRefused(m, e.Position.Index)
				return
			}
			chain = e.ChainSum
		}
		first := m.Entries[start].Position.Index
		if first <= r.log.committed {
			return // never truncate a committed entry
		}
		if first <= r.log.lastIndex() {
			r.truncateFrom(first)
		}
		es := make([]core.Entry, len(m.Entries)-start)
		for k := range es {
			es[k] = m.Entries[start+k]
			es[k].Data = append([]byte(nil), es[k].Data...)
		}
		r.appendEntries(es)
	}
	ack := lastNew
	if r.mutated(mutAckLastIndex) {
		ack = r.log.lastIndex()
	}
	commit := min(m.Commit, lastNew)
	if r.mutated(mutCommitBeyondVerified) {
		commit = min(m.Commit, r.log.lastIndex())
	}
	if commit > r.log.committed {
		r.commitTo(commit)
	}
	r.send(core.Message{Kind: core.MsgAppResp, To: m.From, Index: ack})
}

func (r *Raft) chainRefused(m core.Message, at uint64) {
	if r.refusedAt == at && r.refusedFrom == m.From {
		r.refusals++
	} else {
		r.refusedAt, r.refusedFrom, r.refusals = at, m.From, 1
	}
	if r.cfg.MaxChainRefusals > 0 && r.refusals >= r.cfg.MaxChainRefusals {
		r.refusedLead = m.From
	}
	r.send(core.Message{Kind: core.MsgAppResp, To: m.From, Index: m.Index, Reject: true, RejectHint: m.Index})
}

// maybeEndSilence lifts the silence condition once a leader of a term at
// least the node's own has rewritten its log past the truncation point and
// committed an entry of its current term (R3-09).
func (r *Raft) maybeEndSilence(m core.Message) {
	if r.cfg.SilentIndex == 0 || m.Term < r.cfg.SilentTerm || r.log.lastIndex() < r.cfg.SilentIndex {
		return
	}
	if t, ok := r.log.term(r.log.committed); !ok || t != m.Term || r.log.committed < r.cfg.SilentIndex {
		return
	}
	r.cfg.SilentIndex, r.cfg.SilentTerm = 0, 0
	r.rd.SilenceEnded = true
	// The committed prefix is verified: acknowledging it is safe.
	r.send(core.Message{Kind: core.MsgAppResp, To: m.From, Index: r.log.committed})
}
