package net

// EnvEngine is the variable that caps every group's engine at one capability of
// the lattice: min(code, environment), never above the code's choice.
const EnvEngine string = "KITSUNIUM_NET_ENGINE"

// EnvDisable is the variable that switches engines off, a comma-separated list of
// capabilities the operator may switch off.
const EnvDisable string = "KITSUNIUM_NET_DISABLE"

// Capability names one engine of the lattice the operator's switches restrict
// (ADR 0188), iouring > epoll = kqueue > portable > goroutine, or one optional kernel
// capability a loop group probes (ADR 0189). Its values are the tokens
// KITSUNIUM_NET_ENGINE and KITSUNIUM_NET_DISABLE take; these blocks are the
// registry they are parsed against, and any other token is unknown.
// KITSUNIUM_NET_ENGINE takes an engine only.
type Capability string

// The engines an operator may switch off with KITSUNIUM_NET_DISABLE (ADR 0188).
const (
	// CapabilityIOURing is io_uring, the top of the lattice. Switched off, or capped
	// below, no loop group calls io_uring_setup.
	CapabilityIOURing Capability = "iouring"
	// CapabilityEpoll is epoll, below io_uring. Switched off, a loop group that would
	// have fallen back to it runs on the portable backend.
	CapabilityEpoll Capability = "epoll"
	// CapabilityKqueue is kqueue, at epoll's rank on darwin and the BSDs (ADR 0222).
	// Switched off, or capped below, a loop group there runs on the portable backend
	// and no kqueue is probed.
	CapabilityKqueue Capability = "kqueue"
)

// The floors of the lattice, which KITSUNIUM_NET_ENGINE may name and nothing switches off (ADR 0188).
const (
	// CapabilityPortable is the portable loop backend, the loop family's floor on
	// every platform, and what an unknown token caps the engine at.
	CapabilityPortable Capability = "portable"
	// CapabilityGoroutine is the goroutine engine, the bottom of the lattice. A loop
	// handler capped at it runs on the portable backend, the lowest engine that can
	// serve one.
	CapabilityGoroutine Capability = "goroutine"
)

// The class A-kernel capabilities, on once probed, which KITSUNIUM_NET_DISABLE may switch off (ADR 0189).
const (
	// CapabilityURingMultishotRecv is io_uring's multishot receive. Switched off,
	// every receive is single-shot and re-armed after each completion.
	CapabilityURingMultishotRecv Capability = "uring-multishot-recv"
	// CapabilityURingPbuf is io_uring's provided-buffer ring. The io_uring backend
	// receives into nothing else: switched off, io_uring is never set up and the group
	// runs on epoll.
	CapabilityURingPbuf Capability = "uring-pbuf"
	// CapabilityURingPbufInc is the provided-buffer ring consumed incrementally
	// (IOU_PBUF_RING_INC, 6.12): a buffer serves recv after recv, each from where the
	// last stopped (ADR 0204). Switched off, or refused by the kernel, each recv takes
	// a whole buffer.
	CapabilityURingPbufInc Capability = "uring-pbuf-inc"
	// CapabilityURingDirect is io_uring's direct descriptors, chosen when the release
	// check passes (a kernel from 6.13). Switched off, connections live in the process
	// table and the release check does not run.
	CapabilityURingDirect Capability = "uring-direct"
	// CapabilityEpollExclusive is EPOLLEXCLUSIVE on every loop's listener
	// registration. Switched off, every loop's epoll is woken by each connection.
	CapabilityEpollExclusive Capability = "epoll-exclusive"
	// CapabilityTCPNoDelay is TCP_NODELAY on the listeners a loop group accepts on,
	// inherited by every accepted socket. Switched off, it is not set.
	CapabilityTCPNoDelay Capability = "tcp-nodelay"
	// CapabilityUDPRxqOvfl is SO_RXQ_OVFL on a batched UDP socket (Linux 2.6.33):
	// the kernel attaches its count of the datagrams it dropped for want of
	// receive-buffer room to each message read, which State reports as
	// DatagramStateValue.KernelDrops. Switched off, the option is not set and the
	// count stays zero.
	CapabilityUDPRxqOvfl Capability = "udp-rxq-ovfl"
	// CapabilityUDPPktinfo is IP_PKTINFO and IPV6_RECVPKTINFO on a UDP socket
	// bound to a wildcard address (#972): each datagram names its own destination,
	// read back by Packet.To, and Reply leaves from that address. Off Linux the row
	// reads unprobed. Switched off, the option is not set, Packet.To is the bound
	// address and the kernel chooses a reply's source.
	CapabilityUDPPktinfo Capability = "udp-pktinfo"
	// CapabilityURingRecvmsg is io_uring's multishot RECVMSG (6.0) on a
	// datagram group served by PacketGroup.HandleLoop (#852): each datagram, its
	// sender's address and UDP GRO's segment size land in a provided buffer, and
	// the replies of a batch leave as SENDMSG, UDP_SEGMENT runs included. Absent —
	// a probe stage refused — or switched off, the group's sockets are read by the
	// batched reader of ADR 0192 on the runtime's netpoller, the epoll lane.
	CapabilityURingRecvmsg Capability = "uring-multishot-recvmsg"
	// CapabilityTCPKeepAlive is SO_KEEPALIVE with TCP_KEEPIDLE, TCP_KEEPINTVL and
	// TCP_KEEPCNT on the listeners a loop group accepts on, inherited by every
	// accepted socket: 15 s, 15 s and 9 probes, the standard library's own, so a peer
	// that vanished frees its slot on both engines. Switched off, or set negative,
	// it is not set.
	CapabilityTCPKeepAlive Capability = "tcp-keepalive"
	// CapabilityTCPUserTimeout is TCP_USER_TIMEOUT on the listeners a loop group
	// accepts on, inherited by every accepted socket: the time output may stay
	// unacknowledged before the kernel closes the connection with an error. Switched
	// off, or with no timeout asked for, it is not set.
	CapabilityTCPUserTimeout Capability = "tcp-user-timeout"
	// CapabilityURingSockGetsockopt is io_uring's socket getsockopt command
	// (IORING_OP_URING_CMD with SOCKET_URING_OP_GETSOCKOPT, Linux 6.7), with which
	// the loop reads a Unix connection's SO_PEERCRED on its direct descriptor at
	// accept, so a group with a Unix listener keeps direct descriptors and LoopPeer
	// still answers. Switched off, or absent, a group with a Unix listener runs on
	// process descriptors, where LoopPeer asks the socket.
	CapabilityURingSockGetsockopt Capability = "uring-sock-getsockopt"
)

// The class A-code capabilities, opt-in, which KITSUNIUM_NET_DISABLE may switch off (ADR 0189).
const (
	// CapabilityURingDeferTaskrun is IORING_SETUP_DEFER_TASKRUN with SINGLE_ISSUER on
	// the ring a loop's thread waits on; the other rings keep COOP_TASKRUN.
	CapabilityURingDeferTaskrun Capability = "uring-defer-taskrun"
	// CapabilityURingNoSQArray is IORING_SETUP_NO_SQARRAY: the SQ indexes the SQEs
	// directly, with no indirection array.
	CapabilityURingNoSQArray Capability = "uring-no-sqarray"
	// CapabilityURingRingFDs registers each ring's descriptor with its thread
	// (IORING_REGISTER_RING_FDS), at most 16 a thread, and unregisters it before the
	// ring closes.
	CapabilityURingRingFDs Capability = "uring-ring-fds"
	// CapabilityURingSQRewind is IORING_SETUP_SQ_REWIND with NO_SQARRAY: every
	// submission is filled from SQE 0. It needs the kernel 7.0 lane before it may
	// leave class A-code.
	CapabilityURingSQRewind Capability = "uring-sq-rewind"
	// CapabilityURingRecvBundle is io_uring's receive bundle (IORING_RECVSEND_BUNDLE,
	// 6.10): one completion fills as many provided buffers as the bytes waiting need
	// (ADR 0205). Opted into, on only where a probe recv fills two buffers — not with
	// incremental consumption before 6.15; otherwise each completion fills one buffer.
	CapabilityURingRecvBundle Capability = "uring-recv-bundle"
	// CapabilityTCPDeferAccept is TCP_DEFER_ACCEPT on the listeners a loop group
	// accepts on: a connection is handed to the group once its peer has sent, or the
	// group's read bound has run out, rounded up to whole seconds, so a peer that
	// connects and says nothing takes no slot. Opted into, it is set only where the
	// group has a read bound.
	CapabilityTCPDeferAccept Capability = "tcp-defer-accept"
	// CapabilityReuseportLoops is one SO_REUSEPORT listener per loop of a loop group
	// (#856): opted into with Shards(0), each loop accepts on its own queue, the
	// kernel's four-tuple hash spreading the connections, and the drain takes each
	// loop's queue. Switched off, the group serves one shared listener.
	CapabilityReuseportLoops Capability = "reuseport-loops"
	// CapabilityLoopAffinity pins each loop's thread to the CPU LoopValue.Affinity
	// names for it (#857): a refused CPU leaves the loop unpinned and the row
	// missing, a fault.
	CapabilityLoopAffinity Capability = "loop-affinity"
	// CapabilitySOIncomingCPU is SO_INCOMING_CPU on each per-loop listener of
	// Shards(0) whose loop LoopValue.Affinity pins (#857): the kernel prefers the
	// listener whose CPU received the connection. Opted into, it needs both.
	CapabilitySOIncomingCPU Capability = "so-incoming-cpu"
	// CapabilityTCPNotsentLowat is TCP_NOTSENT_LOWAT on a loop group's TCP
	// listeners, LoopValue.NotsentLowat bytes, 128 KiB when only opted into (#857).
	CapabilityTCPNotsentLowat Capability = "tcp-notsent-lowat"
	// CapabilityHTTP2Loop is the HTTP/2 adapter of a loop group (ADR 0221). Switched
	// off, the group serves HTTP/1.1 alone: the client preface is refused 400 and
	// closed, and State's row reads disabled. It leaves class A-code once its merge
	// gate holds (ADR 0189 §D1).
	CapabilityHTTP2Loop Capability = "http2-loop"
	// CapabilityBusyPoll is busy polling of the network device's receive queues while
	// a loop waits (#979): IORING_REGISTER_NAPI on each io_uring loop's ring 0 and
	// EPIOCSPARAMS on each epoll loop's instance, both Linux 6.9, for
	// LoopValue.BusyPoll with prefer_busy_poll set. It spends CPU to spare the
	// wake-up an interrupt costs; a socket the kernel gives no NAPI id — loopback, a
	// veth without GRO — is not polled. Before 6.9 the row reads absent, and
	// unprobed on the portable backend. It leaves class A-code only through the
	// paired gate of ADR 0189's amendment of 2026-11-11.
	CapabilityBusyPoll Capability = "busy-poll"
)

// The class B capabilities, declared, not built, which KITSUNIUM_NET_DISABLE may name (ADR 0189).
const (
	// CapabilityURingSendZC is io_uring's zero-copy send (IORING_OP_SEND_ZC, Linux
	// 6.0), class A-code since #969 although it sits in this block: a group opts in
	// through LoopValue.Capabilities, every flight of at least LoopValue.SendZCMin
	// bytes then leaves zero-copy, released at the kernel's notification. The
	// probe asks REGISTER_PROBE for the opcode, then sends one datagram zero-copy
	// on loopback and waits for its notification.
	CapabilityURingSendZC Capability = "uring-send-zc"
	// CapabilityURingBundle is io_uring's send bundles; the receive bundle is
	// CapabilityURingRecvBundle's (ADR 0205).
	CapabilityURingBundle Capability = "uring-bundle"
	// CapabilityURingZCRX is io_uring's zero-copy receive (#980): IORING_OP_RECV_ZC
	// (58) into an area registered with IORING_REGISTER_ZCRX_IFQ (32), Linux 6.15, on
	// a ring set up with DEFER_TASKRUN and CQE32, from a NIC receive queue with
	// header and data split that the operator steered to the group's port, the
	// queue registered under CAP_NET_ADMIN. The row names the stage that stops it —
	// the kernel without the opcode, no LoopValue.ZCRX interface, or the register's
	// answer on a throwaway ring — and stays class B, receiving nothing, until its
	// BENCH.md row and threshold exist (ADR 0189, amended 2026-11-11).
	CapabilityURingZCRX Capability = "uring-zcrx"
	// CapabilityReuseportCBPF is a classic BPF program steering a datagram group's
	// SO_REUSEPORT sockets (#977): opted into with Steer, it hands each datagram to
	// the socket of index cpu mod n, the softirq's CPU. Refused — EPERM under a
	// seccomp filter, EINVAL before Linux 4.5 — the row reads absent and the hash
	// spread serves. The stream side, #857, is the loop's to do.
	CapabilityReuseportCBPF Capability = "reuseport-cbpf"
	// CapabilityURingMinTimeout is io_uring's minimum wait (min_wait_usec), only where
	// the kernel advertises IORING_FEAT_MIN_TIMEOUT.
	CapabilityURingMinTimeout Capability = "uring-min-timeout"
	// CapabilityTCPFastOpen is TCP_FASTOPEN on a loop group's listeners (#857): one
	// round trip saved to a repeat client where net.ipv4.tcp_fastopen allows it.
	CapabilityTCPFastOpen Capability = "tcp-fastopen"
)

// CapabilityValue reports one optional kernel capability of a loop group: what
// its probe found and whether the group runs with it (ADR 0189). A capability the
// operator switched off is never probed.
type CapabilityValue struct {
	// Name is the capability.
	Name Capability `json:"name"`
	// Class is "a-kernel", "a-code" or "b".
	Class string `json:"class"`
	// Status is "on" (probed and in use), "available" (probed, not asked for),
	// "absent" (probed, the kernel lacks it), "missing" (a class A-code
	// capability the code opted into and the kernel lacks: a fault),
	// "disabled" (switched off by KITSUNIUM_NET_DISABLE or an unknown token,
	// never probed) or "unprobed" (the backend the group runs on has no use for
	// it).
	Status string `json:"status"`
	// Reason names the probe stage and the kernel's answer, or the switch.
	Reason string `json:"reason,omitempty"`
}
