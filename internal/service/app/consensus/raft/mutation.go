package raft

// mutation reintroduces one cited bug behind a test-only switch (D15). The
// field that holds it is unexported and only the package's own tests set it,
// so no build of the engine can turn one on.
type mutation uint64

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
	// mutForwardNoDedupe proposes a forward each time it is delivered, the
	// duplicate a transport resent included (#401).
	mutForwardNoDedupe
	// mutSilentCampaigns lets a silent node campaign, on its election timer
	// or a TimeoutNow (#524).
	mutSilentCampaigns
	// mutKeptAppended appends the entries Recover kept to a peer instead
	// of sending a snapshot (#507).
	mutKeptAppended
	// mutNoSpaceAdmits admits a command while the no-space alarm is
	// committed (#515).
	mutNoSpaceAdmits
	// mutRefusedLeadHeard keeps answering, and taking as contact, the
	// appends and heartbeats of a leader the node refused for the term
	// after MaxChainRefusals chain refusals (#508).
	mutRefusedLeadHeard
	// mutDenyPinUnchecked admits a DenyPin of this leader, or one that
	// leaves no answering majority without its target (#530).
	mutDenyPinUnchecked
	// mutDenyPinAtAppend withdraws a denied pin from the admission set at
	// append instead of at commit (#530).
	mutDenyPinAtAppend
	// mutReportUnsyncedContract reports in a heartbeat answer the contract
	// generation applied in memory, durable or not (#521).
	mutReportUnsyncedContract
	// mutCampaignInVoteWindow lets the election timer campaign inside the
	// node's own vote-refusal window (#521).
	mutCampaignInVoteWindow
	// mutUnknownGenShortestKnown sizes an acknowledgement of a contract
	// generation the leader does not know as the shortest one it knows
	// (#522).
	mutUnknownGenShortestKnown
	// mutOwnTimingKept keeps the node's own InitialTiming as a known
	// contract after a snapshot named the cluster's (#522).
	mutOwnTimingKept
	// mutLeaseWithoutDrift grants a lease under a contract whose drift
	// budget is not in (0, 1) (#523).
	mutLeaseWithoutDrift
	// mutFoundingAsWritten runs the founding contract as written, its
	// maximum and drift not completed (#523).
	mutFoundingAsWritten
	// mutReadRespAnyPeer takes a read's answer from any peer of the term,
	// not only from the leader this node asked (#510).
	mutReadRespAnyPeer
	// mutMembershipEveryCommit hands the driver the admission set at every
	// commit, changed or not (#531).
	mutMembershipEveryCommit
	// mutMatchPastLastIndex takes an acknowledgement past the leader's last
	// index as the follower's match (#546).
	mutMatchPastLastIndex
	// mutRaiseUnannounced raises the cluster version while a member does not
	// announce it (D18, lot S3).
	mutRaiseUnannounced
	// mutOriginAsWritten keeps the admin payload the proposer wrote, with
	// no origin the leader authenticated (#561).
	mutOriginAsWritten
	// mutHintAnyKind accepts every kind of message from a leader admitted
	// by a leader record alone, a vote included (#542).
	mutHintAnyKind
	// mutRemoveStrands removes an answering member even when a voter may
	// then know no one left to vouch for this leader (#603).
	mutRemoveStrands
	// mutHintEndsWhileHeard ends a leader record its delay after it was
	// taken, while the vouched-for leader is still heard (#618).
	mutHintEndsWhileHeard
	// mutSilenceEndsBeforeCommit ends the silence under the
	// leader of the node's own SilentTerm before the node holds that
	// leader's commit (#636).
	mutSilenceEndsBeforeCommit
	// mutSilenceEndsOnARefusal ends the silence on an append the node
	// refused (#636).
	mutSilenceEndsOnARefusal
	// mutSilenceIndexBelowSnapshot keeps a SilentIndex the node's own
	// snapshot covers at start, so that snapshot's commit ends the silence
	// (#636).
	mutSilenceIndexBelowSnapshot
	// mutSilentReportIgnored ignores the report a silent node's heartbeat
	// answer carries: a stale match is not lowered and the probe never
	// moves past the first batch (#636).
	mutSilentReportIgnored
	// mutRecordAtClusterVersion sends the leader record by the cluster
	// version, as before #638: below 2 never, at 2 to any PreVote.
	mutRecordAtClusterVersion
	// mutRecordUnannounced answers with a leader record a PreVote that does
	// not announce protocol 2 (#638).
	mutRecordUnannounced
	// mutLearnerNeverAsks leaves a learner that hears no leader silent: it
	// never asks its members for the leader's record (#639).
	mutLearnerNeverAsks
	// mutAskCampaigns asks for the leader's record by campaigning: a learner
	// becomes a pre-candidate and counts the grants (#639).
	mutAskCampaigns
)

// mutated reports whether a test switched mutation m on; it is false in every
// build of the engine, whose Config cannot set the field.
func (r *Raft) mutated(m mutation) bool { return r.mut&m != 0 }
