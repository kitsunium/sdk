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
	// KernelDrops is the number of datagrams the kernel dropped before a read
	// could see them, for want of room in a socket's receive buffer: the last
	// SO_RXQ_OVFL count of each batched UDP socket, summed (#973). A gauge of the
	// kernel's own counter, read from the messages that arrive, so a drop after
	// the last message is seen with the next one. The kernel counts what it
	// refused to queue, so under UDP GRO a coalesced message of N datagrams is
	// one drop: the count then stands below the datagrams lost. Zero where the
	// socket is not batched, and under KITSUNIUM_NET_DISABLE=udp-rxq-ovfl.
	KernelDrops uint64 `json:"kernel_drops"`
	// Capabilities reports the optional kernel capabilities of the datagram
	// sockets — udp-rxq-ovfl and udp-pktinfo, and uring-multishot-recvmsg for the
	// groups PacketGroup.HandleLoop serves — as a loop group's do: on, absent, disabled by the
	// operator, or unprobed while no batched socket exists.
	Capabilities []CapabilityValue `json:"capabilities,omitempty"`
}
