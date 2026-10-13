package raft

// mutation reintroduces one cited bug behind a test-only switch (D15). The
// field that holds it is unexported and only the package's own tests set it,
// so no build of the engine can turn one on.
type mutation uint32

const (
	// mutAckBeforeSync acknowledges an append before its sync (raft-sql-poc's
	// unsafeAckBeforeSync).
	mutAckBeforeSync mutation = 1 << iota
	// mutSameTermVoteClear forgets the vote when stepping down in one term.
	mutSameTermVoteClear
	// mutCommitBeyondVerified advances a follower's commit past the prefix
	// the append verified.
	mutCommitBeyondVerified
	// mutAckLastIndex acknowledges lastIndex instead of the verified prefix.
	mutAckLastIndex
	// mutCommitOldTermByCount commits an older term's entry by counting
	// replicas (Figure 8).
	mutCommitOldTermByCount
	// mutNoRestrictionVote drops the election restriction from RequestVote.
	mutNoRestrictionVote
	// mutNoRestrictionPreVote drops it from PreVote.
	mutNoRestrictionPreVote
	// mutTransferAnyTerm honours a transfer flag at any term.
	mutTransferAnyTerm
	// mutHeartbeatUnclamped sends the raw leader commit in a heartbeat.
	mutHeartbeatUnclamped
	// mutNoRestartRefusal votes at once after a restart.
	mutNoRestartRefusal
	// mutTimeoutNowAnyTerm accepts TimeoutNow without the term check.
	mutTimeoutNowAnyTerm
	// mutStaleCompletion counts a storage completion a truncation superseded.
	mutStaleCompletion
	// mutLeaseAfterTransfer keeps the lease after a transfer attempt.
	mutLeaseAfterTransfer
	// mutMembershipBeforeNoOp accepts a membership change before the term's
	// no-op commits.
	mutMembershipBeforeNoOp
	// mutLiveLeaderPreVoteOnly applies the live-leader refusal in PreVote
	// only.
	mutLiveLeaderPreVoteOnly
	// mutRestrictionUnderTransfer skips the election restriction under the
	// transfer flag.
	mutRestrictionUnderTransfer
	// mutLeaseLeaderView computes the lease from the leader's own contract,
	// not the minimum over the acknowledging majority.
	mutLeaseLeaderView
	// mutReadNoRound answers a ReadIndex at once, with no confirmation
	// round: the read a deposed leader serves stale.
	mutReadNoRound
	// mutSnapshotAckLastIndex acknowledges a snapshot install at lastIndex,
	// over a kept suffix nothing verified.
	mutSnapshotAckLastIndex
	// mutInstallBelowBase lets a node with no table install a snapshot
	// below its log's base.
	mutInstallBelowBase
	// mutChainSkip appends an entry whose chain value does not match.
	mutChainSkip
)

// mutated reports whether a test switched mutation m on; it is false in every
// build of the engine, whose Config cannot set the field.
func (r *Raft) mutated(m mutation) bool { return r.mut&m != 0 }
