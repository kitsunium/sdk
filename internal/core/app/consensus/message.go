package consensus

import "github.com/kitsunium/sdk/internal/kernel/errs"

// MessageKind says what a Message is. The two high bits are its lane, so that
// the frame header alone routes it (ADR 0152 D18).
type MessageKind uint8

// The message kinds of protocol version 1.
const (
	// MsgPreVote asks whether the sender COULD win at Term, without raising
	// anybody's term.
	MsgPreVote MessageKind = 0x01
	// MsgPreVoteResp answers a MsgPreVote.
	MsgPreVoteResp MessageKind = 0x02
	// MsgVote asks for a vote at Term.
	MsgVote MessageKind = 0x03
	// MsgVoteResp answers a MsgVote.
	MsgVoteResp MessageKind = 0x04
	// MsgHeartbeat confirms the leader and carries a commit index clamped to
	// the follower's match.
	MsgHeartbeat MessageKind = 0x05
	// MsgHeartbeatResp answers a heartbeat and carries the applied contract
	// generation.
	MsgHeartbeatResp MessageKind = 0x06
	// MsgTimeoutNow tells the transfer target to campaign at once.
	MsgTimeoutNow MessageKind = 0x07
	// MsgReadIndex asks the leader for a confirmed read index.
	MsgReadIndex MessageKind = 0x08
	// MsgReadIndexResp answers a MsgReadIndex.
	MsgReadIndexResp MessageKind = 0x09
	// MsgForwardAdmin forwards an administrative operation to the leader.
	MsgForwardAdmin MessageKind = 0x0A
	// MsgForwardAdminResp answers a MsgForwardAdmin.
	MsgForwardAdminResp MessageKind = 0x0B
	// MsgApp appends entries after (LogTerm, Index).
	MsgApp MessageKind = 0x41
	// MsgAppResp acknowledges, or refuses, a MsgApp.
	MsgAppResp MessageKind = 0x42
	// MsgForward forwards a proposal to the leader under a submission id.
	MsgForward MessageKind = 0x43
	// MsgForwardResp answers a MsgForward with the position it appended at.
	MsgForwardResp MessageKind = 0x44
)

// MessageFlags are a message's boolean qualifiers.
type MessageFlags uint8

// The flags.
const (
	// FlagTransfer marks a vote request of a leadership transfer. It lifts the
	// live-leader refusal for ONE campaign, never the election restriction.
	FlagTransfer MessageFlags = 1 << iota
	// FlagNeedSnapshot marks an append rejection from a node that holds no
	// usable snapshot and asks for one.
	FlagNeedSnapshot
	// FlagNoLease marks a read the follower wants confirmed by a round, never
	// by a lease.
	FlagNoLease
	// FlagTruncated marks an append rejection from a node that truncated
	// acknowledged entries through TruncateAndStaySilent: the leader lowers
	// its match to the node's log, the one exception to "match only grows".
	FlagTruncated
)

// Lane returns the connection the kind belongs on.
func (k MessageKind) Lane() FrameLane { return FrameLane(k >> 6) }

// Known reports whether protocol version 1 defines the kind.
func (k MessageKind) Known() bool {
	return (k >= MsgPreVote && k <= MsgForwardAdminResp) || (k >= MsgApp && k <= MsgForwardResp)
}

// GroupID names a consensus group. v1 runs exactly one group, 1, and every
// frame carries it so that a later version can multiplex groups without
// changing the wire.
type GroupID uint64

// MessageValue is one protocol message: a plain value, encoded by the service's
// AppendMessage and decoded by ParseMessage, never by a method. The engine
// trusts the transport's authenticated peer over From, and drops a message
// whose From differs from it.
type MessageValue struct {
	// Version is the frame layout version, 1.
	Version uint8
	// Group is the consensus group, 1 in v1.
	Group GroupID
	// Kind says what the message is.
	Kind MessageKind
	// Flags qualify it.
	Flags MessageFlags
	// From is the sender, as the sender wrote it.
	From NodeID
	// To is the receiver.
	To NodeID
	// Term is the sender's term.
	Term uint64
	// LogTerm is the term of the entry at Index: the previous entry of an
	// append, the last entry of a vote request.
	LogTerm uint64
	// Index is the previous index of an append, the last index of a vote
	// request, the verified prefix of an acknowledgement, a read index.
	Index uint64
	// Commit is the commit index the sender lets the receiver advance to.
	Commit uint64
	// Reject marks a refusal.
	Reject bool
	// RejectHint is the receiver's last index, a hint where to probe from.
	RejectHint uint64
	// Context correlates a reply with its request: a heartbeat round, a read
	// request, a submission sequence.
	Context uint64
	// TransferIssuer is the leader that issued a transfer flag.
	TransferIssuer NodeID
	// TransferTerm is the term the transfer flag was issued in.
	TransferTerm uint64
	// Contract is the committed contract generation the sender has applied.
	Contract uint64
	// Code is a refusal's error code, 0 for none.
	Code errs.Code
	// Entries are the entries an append carries, or the proposal a forward
	// carries with a zero position.
	Entries []EntryValue
}
