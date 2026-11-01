package net

// EnvEngine is the variable that caps every group's engine at one capability of
// the lattice: min(code, environment), never above the code's choice.
const EnvEngine string = "KITSUNIUM_NET_ENGINE"

// EnvDisable is the variable that switches engines off, a comma-separated list of
// capabilities the operator may switch off.
const EnvDisable string = "KITSUNIUM_NET_DISABLE"

// Capability names one engine of the lattice the operator's switches restrict
// (ADR 0188), iouring > epoll > portable > goroutine, or one optional kernel
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
)

// The class B capabilities, declared, not built, which KITSUNIUM_NET_DISABLE may name (ADR 0189).
const (
	// CapabilityURingSendZC is io_uring's zero-copy send.
	CapabilityURingSendZC Capability = "uring-send-zc"
	// CapabilityURingBundle is io_uring's receive and send bundles.
	CapabilityURingBundle Capability = "uring-bundle"
	// CapabilityBusyPoll is busy polling of the network device's queue.
	CapabilityBusyPoll Capability = "busy-poll"
	// CapabilityURingZCRX is io_uring's zero-copy receive.
	CapabilityURingZCRX Capability = "uring-zcrx"
	// CapabilityReuseportCBPF is a classic BPF program steering SO_REUSEPORT
	// connections to their loop.
	CapabilityReuseportCBPF Capability = "reuseport-cbpf"
	// CapabilityTCPDeferAccept is TCP_DEFER_ACCEPT on the listeners.
	CapabilityTCPDeferAccept Capability = "tcp-defer-accept"
	// CapabilityURingMinTimeout is io_uring's minimum wait (min_wait_usec), only where
	// the kernel advertises IORING_FEAT_MIN_TIMEOUT.
	CapabilityURingMinTimeout Capability = "uring-min-timeout"
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
