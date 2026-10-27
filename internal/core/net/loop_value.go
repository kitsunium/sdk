package net

// LoopBackend names the mechanic a loop group runs on (ADR 0186 §D5). The zero
// value is LoopBackendAuto.
type LoopBackend uint8

// The loop backends. The zero value is LoopBackendAuto.
const (
	// LoopBackendAuto takes the best backend the platform's probe reaches:
	// io_uring, then epoll on Linux, the portable backend elsewhere.
	LoopBackendAuto LoopBackend = iota
	// LoopBackendIOURing is io_uring, on linux/amd64, arm64, riscv64 and loong64,
	// after the kernel's staged probe.
	LoopBackendIOURing
	// LoopBackendEpoll is epoll, on every Linux architecture.
	LoopBackendEpoll
	// LoopBackendPortable is the default engine's accept path and goroutines,
	// serialised onto one handler goroutine, on every platform.
	LoopBackendPortable
)

// String renders the backend as State and the start line spell it.
func (b LoopBackend) String() string {
	//: a dense switch, so a constant added without its name falls to default
	//: and the test that walks the block names it.
	switch b {
	//: the probe decides.
	case LoopBackendAuto:
		//: the spelling State and the start line use.
		return "auto"
	//: io_uring.
	case LoopBackendIOURing:
		//: the spelling State and the start line use.
		return "iouring"
	//: epoll.
	case LoopBackendEpoll:
		//: the spelling State and the start line use.
		return "epoll"
	//: the default engine's accept path, serialised.
	case LoopBackendPortable:
		//: the spelling State and the start line use.
		return "portable"
	//: an out-of-range value is reported rather than hidden.
	default:
		//: report the out-of-range value rather than hiding it.
		return "unknown"
	}
}

// LoopValue configures a loop group's engine (ADR 0186 §D5). It is a value of
// its own, passed by the Loop group option, so LimitsValue and TimeoutsValue
// keep the meanings they publish for every engine. Every field is optional:
// zero selects the domain default, never "unbounded".
type LoopValue struct {
	// Backend is the mechanic to run on. LoopBackendAuto takes the best one the
	// platform's probe reaches and reports any fallback in State; an explicit
	// backend the platform cannot run is refused at Start.
	Backend LoopBackend `json:"backend"`
	// Loops is the number of loops, each one goroutine locked to its OS thread
	// on a native backend. Zero selects one. The SDK never sets GOMAXPROCS:
	// sizing the process against its loops is the consumer's policy, and State
	// reports both.
	Loops int `json:"loops"`
	// Rings is the number of descriptor shards inside one loop, each with its
	// own table of at most min(RLIMIT_NOFILE, IORING_MAX_FIXED_FILES) entries:
	// what lets io_uring serve more connections than RLIMIT_NOFILE. Zero
	// derives it, ⌈max(MaxConns, SoftConns) / min(RLIMIT_NOFILE,
	// IORING_MAX_FIXED_FILES)⌉, and one when both are zero; that is the only
	// formula, and State says where the value came from. epoll and the portable
	// backend run one ring whatever it says.
	Rings int `json:"rings"`
	// CQBatch bounds the completions or events one loop iteration reaps before
	// it calls OnBatchEnd. Zero selects the domain default.
	CQBatch int `json:"cq_batch"`
	// SpillCap is the most unconsumed input, in bytes, one connection may hold
	// between OnData calls. Past it the connection is closed with
	// LoopSpillFull. Zero selects the domain default.
	SpillCap int `json:"spill_cap"`
	// OutputCap is the most written and unsent output, in bytes, one
	// connection may hold. A Write past it is refused with LoopOutputFull. Zero
	// selects the domain default.
	OutputCap int `json:"output_cap"`
	// SoftConns is the soft connection bound: a connection accepted while the
	// group serves more is flagged under pressure (LoopIO.Pressure), so a
	// protocol can ask its peer to leave. It refuses nothing; MaxConns does.
	// Zero sets no soft bound.
	SoftConns int `json:"soft_conns"`
	// EvictIdle is how long a connection must have been idle before the loop
	// may close it to admit another under hard pressure — the process out of
	// descriptors, or MaxConns reached. A connection with unread bytes is
	// never evicted. Zero evicts nothing.
	EvictIdle DurationValue `json:"evict_idle"`
	// Capabilities opts the group into kernel capabilities of class A-code
	// (ADR 0189). Each is probed and used where the kernel has it, reported
	// "missing" and as a fault where it does not, and never used once
	// KITSUNIUM_NET_DISABLE names it. An engine, a class B capability or an
	// unknown token is refused at Start with LoopCapabilityRefused. Nil asks
	// for none; the class A-kernel capabilities need no opt-in.
	Capabilities []Capability `json:"capabilities,omitempty"`
	// Outbound is how many jobs one lane's queue to its worker holds (R3),
	// when the group's handler offloads (LoopOffloader). A Send past it is
	// refused with LoopOutboundFull and counted in LoopStateValue. Zero
	// selects 256.
	Outbound int `json:"outbound"`
}
