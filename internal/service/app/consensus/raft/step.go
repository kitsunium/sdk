package raft

import (
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// Step handles one message. The driver has already checked that m.From is
// the peer the transport authenticated (R7-05).
func (r *Raft) Step(now time.Time, m core.MessageValue) {
	r.now = now
	if m.Group != r.cfg.Group || r.stepLeaderRecord(now, m) || !r.stepTerm(now, m) || r.stepAnyRole(now, m) {
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

// stepTerm reconciles m's term with this node's before anything else reads
// m: a higher term makes it a follower, unless the message is one a higher
// term must not impose; an older term is answered so its sender steps down.
// It reports whether m goes on to be handled at this node's term.
func (r *Raft) stepTerm(now time.Time, m core.MessageValue) bool {
	switch {
	case m.Term == 0 || m.Term == r.term:
		// Local-only kinds carry no term; a message of this term goes on.
		return true
	case m.Term > r.term:
		return r.stepHigherTerm(now, m)
	default:
		r.answerOlderTerm(m)
		return false
	}
}

// stepHigherTerm adopts the higher term m carries, or refuses it; it
// reports whether m goes on to be handled.
func (r *Raft) stepHigherTerm(now time.Time, m core.MessageValue) bool {
	// TimeoutNow is honoured only at the receiver's own term (R2-13), and
	// a vote refused for a live leader never moves a term (R1-18).
	if (m.Kind == core.MsgTimeoutNow && !r.mutated(mutTimeoutNowAnyTerm)) ||
		((m.Kind == core.MsgPreVote || m.Kind == core.MsgVote) && r.refuseForLiveLeader(now, m)) {
		return false
	}
	r.adoptTerm(m)
	return true
}

// adoptTerm makes this node a follower at m's higher term, following its
// sender when m comes from a leader. A PreVote, and the granted answer to
// one, carry a prospective term and move nothing.
func (r *Raft) adoptTerm(m core.MessageValue) {
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
}

// answerOlderTerm tells the sender of an older term's append, heartbeat or
// PreVote of the current term; anything else of an older term is dropped
// unanswered.
func (r *Raft) answerOlderTerm(m core.MessageValue) {
	switch m.Kind {
	case core.MsgApp, core.MsgHeartbeat:
		// A leader of an older term learns of ours, so that it steps
		// down and a PreVote round decides (R1-18).
		r.send(core.MessageValue{Kind: core.MsgAppResp, To: m.From, Reject: true, Index: m.Index})
	case core.MsgPreVote:
		r.send(core.MessageValue{Kind: core.MsgPreVoteResp, To: m.From, Term: r.term, Reject: true})
	default:
		// Nothing else of an older term needs an answer.
	}
}

// stepAnyRole handles the kinds every role handles the same way: votes,
// forwarded proposals and the answers to this node's own forwards and
// reads. It reports whether it handled m.
func (r *Raft) stepAnyRole(now time.Time, m core.MessageValue) bool {
	switch m.Kind {
	case core.MsgPreVote, core.MsgVote:
		r.handleVote(now, m)
		return true
	case core.MsgForward, core.MsgForwardAdmin:
		r.handleForward(m)
		return true
	case core.MsgReadIndexResp:
		// Only the leader this node asked answers its reads: another
		// peer's answer, of this term, is not one (#510).
		if m.From == r.lead || r.mutated(mutReadRespAnyPeer) {
			r.rd.ReadStates = append(r.rd.ReadStates, ReadState{Context: m.Context, Index: m.Index, Code: m.Code})
		}
		return true
	case core.MsgForwardResp, core.MsgForwardAdminResp:
		r.rd.Forwarded = append(r.rd.Forwarded, m)
		return true
	default:
		// The rest depends on the role.
		return false
	}
}

// refuseForLiveLeader is D5's vote-side CheckQuorum: a node that heard from
// a live leader within the window, or that just started, refuses a vote of
// a higher term unless the request carries a valid transfer flag. A leader
// refuses too while a majority answers it (R7-19).
func (r *Raft) refuseForLiveLeader(now time.Time, m core.MessageValue) bool {
	if (r.mutated(mutLiveLeaderPreVoteOnly) && m.Kind == core.MsgVote) || r.validTransferFlag(m) {
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
func (r *Raft) validTransferFlag(m core.MessageValue) bool {
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

// upToDate is the election restriction: m's log is at least as recent as
// this node's, by last term and then by last index.
func (r *Raft) upToDate(m core.MessageValue) bool {
	lt := r.log.lastTerm()
	return m.LogTerm > lt || (m.LogTerm == lt && m.Index >= r.log.lastIndex())
}

// handleVote answers a vote or a PreVote of this node's term, after the
// live-leader refusal already passed. Only a real vote is recorded, and its
// grant leaves after the vote is durable (send, D5b).
func (r *Raft) handleVote(now time.Time, m core.MessageValue) {
	pre := m.Kind == core.MsgPreVote
	resp := core.MsgVoteResp
	if pre {
		resp = core.MsgPreVoteResp
	}
	canVote := r.mayGrant(pre, m)
	restricted := r.voteRestricted(pre, m)
	ok := canVote && r.cfg.CanVote && r.cfg.SilentIndex == 0 && (!restricted || r.upToDate(m))
	if !ok {
		r.send(core.MessageValue{Kind: resp, To: m.From, Term: r.term, Reject: true})
		return
	}
	if pre {
		// A PreVote's prospective term and vote are never persisted (R2-03).
		r.send(core.MessageValue{Kind: resp, To: m.From, Term: m.Term})
		return
	}
	r.vote = m.From
	r.resetElection(now)
	r.send(core.MessageValue{Kind: resp, To: m.From, Term: m.Term})
}

// mayGrant reports whether this node is free to grant m: it already voted
// for the sender, or it has neither voted nor a leader this term, or m is a
// PreVote for a later term, which binds nothing.
func (r *Raft) mayGrant(pre bool, m core.MessageValue) bool {
	return r.vote == m.From ||
		(r.vote == 0 && r.lead == 0) ||
		(pre && m.Term > r.term)
}

// voteRestricted reports whether the election restriction applies to m: it
// always does, unless a test mutation lifts it.
func (r *Raft) voteRestricted(pre bool, m core.MessageValue) bool {
	switch {
	case pre && r.mutated(mutNoRestrictionPreVote),
		!pre && r.mutated(mutNoRestrictionVote),
		m.Flags&core.FlagTransfer != 0 && r.mutated(mutRestrictionUnderTransfer):
		return false
	default:
		return true
	}
}

// ---- roles ----

// becomeFollower moves to term, following lead (0 when unknown). The vote
// is forgotten only with a new term, and a deposed leader refuses what it
// still held for its clients.
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
	r.selfVoteSeq = 0
	r.facts &^= raftSelfVoteDurable
	r.campaignIssuer, r.campaignTerm = 0, 0
	r.facts &^= raftLeaseDropped
	r.leaseStart = time.Time{}
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
		if r.facts&raftTransferSent == 0 {
			res.Code = codeTransferFailed
		}
		r.rd.Transfers = append(r.rd.Transfers, res)
		r.transferTo = 0
	}
	r.prs = map[core.NodeID]*progress{}
}

// campaign starts a PreVote round (pre) or an election at the next term.
// A PreVote round moves no term and persists nothing; an election votes for
// itself, which counts once durable (D5b), and carries the transfer flag of
// issuer for issuerTerm when a TimeoutNow started it.
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
			r.send(core.MessageValue{
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
	r.facts &^= raftSelfVoteDurable
	r.campaignIssuer, r.campaignTerm = issuer, issuerTerm
	r.resetElection(r.now)
	for _, id := range r.voters() {
		if id == r.id {
			continue
		}
		m := core.MessageValue{Kind: core.MsgVote, To: id, LogTerm: r.log.lastTerm(), Index: r.log.lastIndex()}
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
	granted, rejected := r.countVotes()
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

// countVotes counts the current voters' answers to the campaign; a voter
// that has not answered counts in neither total.
func (r *Raft) countVotes() (granted, rejected int) {
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
	return granted, rejected
}

// stepCandidate handles a message of the campaign's term: an append or a
// heartbeat means a leader won it, an answer is tallied, a read is refused.
func (r *Raft) stepCandidate(now time.Time, m core.MessageValue) {
	switch m.Kind {
	case core.MsgApp, core.MsgHeartbeat:
		r.followWinner(now, m)
	case core.MsgPreVoteResp:
		if r.role == core.RolePreCandidate {
			r.votes[m.From] = !m.Reject
			r.tallyVotes()
		}
	case core.MsgVoteResp:
		if r.role == core.RoleCandidate {
			r.votes[m.From] = !m.Reject
			if r.facts&raftSelfVoteDurable != 0 {
				r.tallyVotes()
			}
		}
	case core.MsgReadIndex:
		r.send(core.MessageValue{Kind: core.MsgReadIndexResp, To: m.From, Context: m.Context, Code: codeNotLeader})
	default:
		// Nothing else concerns a campaign: it is dropped.
	}
}

// followWinner follows the leader of the campaign's term that m comes
// from, unless this node refused that leader for the term: its campaign is
// how the refusal ends, and the leader's messages do not end it (#508).
func (r *Raft) followWinner(now time.Time, m core.MessageValue) {
	if r.refuses(m.From) {
		return
	}
	r.becomeFollower(m.Term, m.From)
	r.stepFollower(now, m)
}

// stepFollower handles a message of the leader's term at a follower or a
// learner; an append or a heartbeat is contact with the leader, and extends
// the record of a vouched-for one (#618), unless this node refused that
// leader for the term.
func (r *Raft) stepFollower(now time.Time, m core.MessageValue) {
	if (m.Kind == core.MsgApp || m.Kind == core.MsgHeartbeat) && r.refuses(m.From) {
		// After N chain refusals the leader's appends and heartbeats are
		// neither answered nor taken as contact for the rest of the term
		// (D4, R6-05). Answered, every rejection counted as contact for
		// CheckQuorum and the leader resent at once, in a loop that never
		// ended; taken as contact, they kept the refusing majority from
		// ever campaigning, and the leader was never deposed (#508).
		return
	}
	switch m.Kind {
	case core.MsgApp:
		r.followAppend(now, m)
	case core.MsgHeartbeat:
		r.followHeartbeat(now, m)
	case core.MsgTimeoutNow:
		r.followTimeoutNow(m)
	case core.MsgReadIndex:
		r.send(core.MessageValue{Kind: core.MsgReadIndexResp, To: m.From, Context: m.Context, Code: codeNotLeader})
	default:
		// Nothing else concerns a follower: it is dropped.
	}
}

// followAppend records the leader's contact and handles its append; only
// an append this node accepted may end a silence (#636).
func (r *Raft) followAppend(now time.Time, m core.MessageValue) {
	r.lead = m.From
	r.lastLeaderContact = now
	r.extendHint(now, m.From)
	r.resetElection(now)
	if r.handleAppend(m) || r.mutated(mutSilenceEndsOnARefusal) {
		r.maybeEndSilence(m)
	}
}

// followHeartbeat records the leader's contact, commits what the heartbeat
// allows of the entries held, and answers the round.
func (r *Raft) followHeartbeat(now time.Time, m core.MessageValue) {
	r.lead = m.From
	r.lastLeaderContact = now
	r.extendHint(now, m.From)
	r.resetElection(now)
	if c := min(m.Commit, r.log.lastIndex()); c > r.log.committed {
		r.commitTo(c)
	}
	resp := core.MessageValue{
		Kind: core.MsgHeartbeatResp, To: m.From, Context: m.Context, Contract: r.reportedContract(),
		Index: r.heldOfTerm(),
	}
	r.reportSilence(&resp)
	r.send(resp)
	if r.cfg.NoTable {
		// A node with no table asks for a snapshot on every heartbeat, as
		// on an append (D7, R7-18): a leader with nothing to append to it
		// sends only heartbeats, and the node held no state for as long as
		// the cluster stayed quiet (#549). The leader ignores the ask while
		// a transfer to it is under way.
		r.send(core.MessageValue{
			Kind: core.MsgAppResp, To: m.From, Index: r.log.lastIndex(), Reject: true,
			RejectHint: r.log.lastIndex(), Flags: core.FlagNeedSnapshot,
		})
	}
}

// heldOfTerm is the index a heartbeat answer reports: the last index when
// that entry is of this term, so this node holds the leader's log through
// it (Log Matching), durable or not; 0 otherwise, read as nothing
// reported. The leader tells a slow acknowledgement from a lost append by
// it (#560).
func (r *Raft) heldOfTerm() uint64 {
	last := r.log.lastIndex()
	if t, ok := r.log.term(last); ok && t == r.term {
		return last
	}
	return 0
}

// reportSilence marks the heartbeat answer of a silent node with
// FlagTruncated (#636). RejectHint is the end of its durable log: the
// leader lowers its match to it, as on a refused append, so a match that
// still counts entries the truncation dropped is lowered at the first
// answer, before any append. Commit is its commit, through which it holds
// the leader's log: a leader probing it resumes past that index. A silent
// node acknowledges no append, so the probe never moved and the leader
// resent the batch after the truncation point at every heartbeat; the node,
// which needs the commit of a leader of its own term to vote again, never
// got past that batch. A leader of a release that does not read them
// ignores the flag and both fields on a heartbeat answer.
func (r *Raft) reportSilence(m *core.MessageValue) {
	if r.cfg.SilentIndex == 0 {
		return
	}
	m.Flags |= core.FlagTruncated
	m.RejectHint = min(r.log.lastIndex(), r.log.stable)
	m.Commit = r.log.committed
}

// reportedContract is the contract generation a heartbeat answer reports:
// the one the committed entries this node holds durably put in force, the
// one a restart rebuilds. The leader sizes its lease on the window of that
// generation, and the node must refuse votes for at least that long after a
// crash. Reporting the generation applied in memory, before the contract's
// entry or the commit that applied it was durable, let a restarted node
// fall back to the older, shorter window and vote or campaign inside the
// lease it had granted (#521, R3-07).
func (r *Raft) reportedContract() uint64 {
	if r.mutated(mutReportUnsyncedContract) {
		return r.timing.Generation
	}
	return r.EngineAt(min(r.log.committed, r.log.stable)).Timing.Generation
}

// followTimeoutNow starts an election at once, carrying the transfer flag,
// when the leader of this term hands leadership over and this node may
// campaign.
func (r *Raft) followTimeoutNow(m core.MessageValue) {
	// Accepted only from the leader of this term, at this term (R2-13).
	valid := r.mutated(mutTimeoutNowAnyTerm) || (m.From == r.lead && m.Term == r.term)
	// A node whose apply the driver holds stalled takes no leadership by a
	// transfer: it handed its own over for that apply (#554), and a
	// transfer back to it made it lead almost all the time (#674).
	stalled := r.facts&raftApplyStalled != 0 && !r.mutated(mutStalledTakesOver)
	if valid && !stalled && r.promotable() {
		r.campaign(false, m.From, m.Term)
	}
}

// handleAppend is the follower's side of AppendEntries, with the three rules
// raft-sql-poc learned as safety bugs (D6). It reports whether the append
// was accepted: its prefix matched this log, verified, and was acknowledged.
// A refused or dropped append proves nothing about this log, and only an
// accepted one may end a silence (#636).
func (r *Raft) handleAppend(m core.MessageValue) bool {
	if r.cfg.NoTable {
		// No usable snapshot: it asks for one instead of storing entries it
		// could never apply (D7, R7-18).
		r.send(core.MessageValue{
			Kind: core.MsgAppResp, To: m.From, Index: m.Index, Reject: true,
			RejectHint: r.log.lastIndex(), Flags: core.FlagNeedSnapshot,
		})
		return false
	}
	if !r.rebaseOnCommit(&m) {
		return false
	}
	if !r.log.matchTerm(m.Index, m.LogTerm) {
		hint := min(m.Index, r.log.lastIndex())
		r.send(core.MessageValue{Kind: core.MsgAppResp, To: m.From, Index: m.Index, Reject: true, RejectHint: hint})
		return false
	}
	r.dropUnknownKinds(&m)
	start, prevChain, ok := r.firstNewEntry(&m)
	if !ok || (start >= 0 && !r.appendNew(&m, start, prevChain)) {
		return false
	}
	r.acknowledgeAppend(&m)
	return true
}

// rebaseOnCommit handles an append that starts below the commit. Its
// entries past the commit are still news: it is re-based on the committed
// entry when it carries that entry with our term. A node that acknowledges
// nothing — a silent one — learns the rest of the log no other way. It
// reports false, having answered with the commit, when m cannot be re-based.
func (r *Raft) rebaseOnCommit(m *core.MessageValue) bool {
	if m.Index >= r.log.committed {
		return true
	}
	k := int(r.log.committed - m.Index - 1)
	t, ok := r.log.term(r.log.committed)
	if k >= len(m.Entries) || !ok || m.Entries[k].Position.Term != t {
		r.send(core.MessageValue{Kind: core.MsgAppResp, To: m.From, Index: r.log.committed})
		return false
	}
	m.Entries = m.Entries[k+1:]
	m.Index, m.LogTerm = r.log.committed, t
	return true
}

// dropUnknownKinds cuts m's entries at the first one whose kind this
// version does not know and names it in the Ready: that entry and the ones
// after it are never appended, acknowledged or skipped, the ones before it
// are, and the driver stops the node with VERSION_UNSUPPORTED (review M3).
func (r *Raft) dropUnknownKinds(m *core.MessageValue) {
	for k := range m.Entries {
		if e := &m.Entries[k]; !e.Kind.Known() {
			r.rd.Unsupported = UnsupportedEntryValue{Index: e.Position.Index, Kind: e.Kind}
			m.Entries = m.Entries[:k]
			break
		}
	}
}

// firstNewEntry skips the entries of m the log already holds at the same
// term and returns the position in m.Entries of the first one it does not,
// -1 when it holds them all, with the chain value before it. ok is false,
// and the append is dropped unanswered, when the entries are not one per
// index after m.Index.
func (r *Raft) firstNewEntry(m *core.MessageValue) (start int, prevChain uint32, ok bool) {
	prevChain = r.log.chain(m.Index)
	for k := range m.Entries {
		e := &m.Entries[k]
		if e.Position.Index != m.Index+uint64(k)+1 {
			return -1, prevChain, false
		}
		if t, held := r.log.term(e.Position.Index); held && t == e.Position.Term {
			prevChain = e.ChainSum
			continue
		}
		return k, prevChain, true
	}
	return -1, prevChain, true
}

// appendNew verifies and appends m's entries from start on, truncating a
// conflicting suffix first. It reports false, and the append goes
// unacknowledged, when a body or a chain does not verify or when the
// conflict would truncate a committed entry.
func (r *Raft) appendNew(m *core.MessageValue, start int, prevChain uint32) bool {
	if !r.verifyChain(m, start, prevChain) {
		return false
	}
	first := m.Entries[start].Position.Index
	if first <= r.log.committed {
		return false // never truncate a committed entry
	}
	if first <= r.log.lastIndex() {
		r.truncateFrom(first)
	}
	es := make([]core.EntryValue, len(m.Entries)-start)
	for k := range es {
		es[k] = m.Entries[start+k]
		es[k].Data = ownCopy(es[k].Data)
	}
	r.appendEntries(es)
	return true
}

// verifyChain checks the body and the chain of m's entries from start on
// before anything is appended: a mismatch refuses the acknowledgement, and
// the leader resends (D4).
func (r *Raft) verifyChain(m *core.MessageValue, start int, prevChain uint32) bool {
	chain := prevChain
	for k := start; k < len(m.Entries); k++ {
		e := &m.Entries[k]
		if !r.mutated(mutChainSkip) && (wire.BodySum(e.Data) != e.BodySum || wire.ChainSum(chain, e) != e.ChainSum) {
			r.chainRefused(*m, e.Position.Index)
			return false
		}
		chain = e.ChainSum
	}
	return true
}

// acknowledgeAppend acknowledges the prefix m verified and commits up to
// the leader's commit within it: never past what this append proved
// matches the leader's log.
func (r *Raft) acknowledgeAppend(m *core.MessageValue) {
	lastNew := m.Index + uint64(len(m.Entries))
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
	r.send(core.MessageValue{Kind: core.MsgAppResp, To: m.From, Index: ack})
}

// chainRefused counts a refused append from m's sender at index at and
// answers it with a rejection; after MaxChainRefusals in a row at one
// index, that leader's heartbeats go unanswered for the term (D4, R6-05).
func (r *Raft) chainRefused(m core.MessageValue, at uint64) {
	if r.refusedAt == at && r.refusedFrom == m.From {
		r.refusals++
	} else {
		r.refusedAt, r.refusedFrom, r.refusals = at, m.From, 1
	}
	if r.cfg.MaxChainRefusals > 0 && r.refusals >= r.cfg.MaxChainRefusals {
		r.refusedLead, r.refusedTerm = m.From, r.term
	}
	r.rd.ChainRefusedBy = append(r.rd.ChainRefusedBy, m.From)
	r.send(core.MessageValue{Kind: core.MsgAppResp, To: m.From, Index: m.Index, Reject: true, RejectHint: m.Index})
}

// refuses reports whether this node refused the leader from for the rest
// of the current term, after MaxChainRefusals refusals of its chain.
func (r *Raft) refuses(from core.NodeID) bool {
	return r.refusedLead != 0 && r.refusedLead == from && r.refusedTerm == r.term && !r.mutated(mutRefusedLeadHeard)
}

// maybeEndSilence lifts the silence condition, on an append this node
// accepted, once a leader of a term at least the node's own has rewritten
// its log past the truncation point and committed an entry of its current
// term (R3-09), and, when that term is the node's own SilentTerm, once the
// node holds that leader's commit (#636).
//
// A leader of a later term was elected without this node, which granted no
// vote while silent and named no later term before the damage: every entry
// committed before that term, those this node acknowledged and lost
// included, is in that leader's log before its first entry of its term, so
// committing an entry of that term proves the node holds them all. The
// leader of the node's own term is the one that committed the lost entries
// with this node's acknowledgements, so its first entries past the
// truncation point prove nothing: one batch committed an entry of its term
// while entries the node had acknowledged, and that leader had committed,
// were still missing, the node voted again without them, and one more
// failure of that leader elected a log that lacked them. Holding m.Commit,
// the node holds everything that leader had committed when it sent m, and
// the election restriction then refuses any candidate that lacks it.
func (r *Raft) maybeEndSilence(m core.MessageValue) {
	if r.cfg.SilentIndex == 0 || m.Term < r.cfg.SilentTerm || r.log.lastIndex() < r.cfg.SilentIndex {
		return
	}
	if t, ok := r.log.term(r.log.committed); !ok || t != m.Term || r.log.committed < r.cfg.SilentIndex {
		return
	}
	if m.Term == r.cfg.SilentTerm && r.log.committed < m.Commit && !r.mutated(mutSilenceEndsBeforeCommit) {
		return
	}
	r.cfg.SilentIndex, r.cfg.SilentTerm = 0, 0
	r.rd.SilenceEnded = true
	// The committed prefix is verified: acknowledging it is safe.
	r.send(core.MessageValue{Kind: core.MsgAppResp, To: m.From, Index: r.log.committed})
}
