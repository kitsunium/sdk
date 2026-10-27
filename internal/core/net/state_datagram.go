package net

// DatagramStateValue counts what the batched datagram paths drop (ADR 0192):
// the messages a read cut short, and the replies a batch queued that did not
// leave. Each is a drop nobody above a read loop could be told of, so the
// counter is the report.
type DatagramStateValue struct {
	// TruncatedPackets is the number of messages dropped because the kernel cut
	// them: a coalesced message past its buffer (MSG_TRUNC under UDP GRO), or a
	// message whose control data did not fit (MSG_CTRUNC). Neither can be split
	// into whole datagrams, so none of what survives is delivered.
	TruncatedPackets uint64 `json:"truncated_packets"`
	// GSOOversized is the number of GSO sends the kernel refused with EMSGSIZE:
	// a sizing bug, counted, which leaves GSO on.
	GSOOversized uint64 `json:"gso_oversized"`
	// ReplyFailures is the number of replies a BatchReplies group queued during
	// a batch that did not leave: Reply had already reported them sent.
	ReplyFailures uint64 `json:"reply_failures"`
}
