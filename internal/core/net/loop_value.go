package net

// LoopsAuto, set as LoopValue.Loops, runs one loop per GOMAXPROCS but one, at
// least one, read once at Start: a snapshot that a later change of GOMAXPROCS
// does not move. The P left over runs the rest of the process (ADR 0199). It
// is a sentinel, never the zero value, which is one loop (ADR 0187 §D9); State
// reports the count and "auto" as its source.
const LoopsAuto int = -1

// LoopBackend names the mechanic a loop group runs on (ADR 0186 §D5). The zero
// value is LoopBackendAuto.
type LoopBackend uint8

// The loop backends. The zero value is LoopBackendAuto.
const (
	// LoopBackendAuto takes the best backend the platform's probe reaches:
	// io_uring, then epoll on Linux, the portable backend elsewhere. On darwin
	// and the BSDs it stays portable until kqueue's paired gate holds: kqueue
	// runs there when named, LoopBackendKqueue (ADR 0222).
	LoopBackendAuto LoopBackend = iota
	// LoopBackendIOURing is io_uring, on linux/amd64, arm64, riscv64 and loong64,
	// after the kernel's staged probe.
	LoopBackendIOURing
	// LoopBackendEpoll is epoll, on every Linux architecture.
	LoopBackendEpoll
	// LoopBackendPortable is the default engine's accept path and goroutines,
	// serialised onto one handler goroutine, on every platform.
	LoopBackendPortable
	// LoopBackendKqueue is kqueue, on darwin, FreeBSD, OpenBSD, NetBSD and
	// DragonFly, after a staged probe (ADR 0222). It runs only when named:
	// LoopBackendAuto does not choose it until its paired gate holds.
	LoopBackendKqueue
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
	//: kqueue, on darwin and the BSDs.
	case LoopBackendKqueue:
		//: the spelling State and the start line use.
		return "kqueue"
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
	// on a native backend. Zero selects one; LoopsAuto selects GOMAXPROCS less
	// one, at least one, read once at Start. The SDK never sets GOMAXPROCS:
	// sizing the process against its loops is the consumer's policy, and State
	// reports both. A native loop yields to the Go scheduler every 5 ms of
	// serving while GOMAXPROCS exceeds the group's loops (ADR 0199).
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
	// LoopSpillFull. Zero selects the domain default. It is the ceiling on a
	// request's head and body together for an http1 handler that takes the
	// body whole (a LoopSpillChecker adapts to it at Start); an
	// HTTP1BodyReader's spill holds only the head.
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
	// BatchWait bounds how long an io_uring loop holds its wait for
	// BatchWaitMin completions while it is loaded: its previous batch reaped
	// at least four, or two receives carrying bytes at least come within the
	// bound at the rate they arrive. A loaded loop so serves larger batches for fewer
	// wakeups (ADR 0199, ADR 0202). It
	// bounds each grouped wait, not a request's latency: a request read over
	// several batches may meet one at each. Zero selects 250 µs,
	// a negative value turns the grouped wait off, and anything past the 10 ms
	// tick is the tick. epoll and the portable backend ignore it.
	BatchWait DurationValue `json:"batch_wait"`
	// BatchWaitMin is how many completions the grouped wait waits for before
	// BatchWait runs out. Zero selects 32; past CQBatch it is CQBatch.
	BatchWaitMin int `json:"batch_wait_min"`
	// BatchBudget bounds how long an io_uring loop reaps completions in one
	// batch: past it, a ring stops reaping, and a ring after it reaps at most one
	// more lot of 16 before it stops too — a bounded overrun, not a hard stop.
	// What is left waits for the next batch, which starts at the ring it stopped
	// at and does not wait. A
	// request is answered once its batch ends, after OnBatchEnd, so a batch of
	// thousands of requests holds every answer for its whole length (ADR 0206).
	// A batch that ran past half of it hands its answers to the kernel at once
	// instead of at the next wait. Zero, or a negative value, bounds nothing:
	// the batch reaps up to four CQBatch a ring, as before. epoll and the
	// portable backend ignore it.
	BatchBudget DurationValue `json:"batch_budget"`
	// SendZCMin is the smallest flight an io_uring loop sends zero-copy, in
	// bytes, once the group opted into uring-send-zc (#969): a flight of at least
	// that many bytes leaves by IORING_OP_SEND_ZC and its slab chunk stays the
	// kernel's until the notification says the pages are free, a shorter one by
	// IORING_OP_SEND as before. Zero selects 16 KiB; a negative value is refused
	// with LoopValueInvalid. epoll and the portable backend ignore it, and so
	// does a group that did not opt in.
	SendZCMin int `json:"send_zc_min,omitempty"`
	// Affinity pins each loop's thread to one CPU, Affinity[i] for loop i, with
	// sched_setaffinity once the thread is locked (loop-affinity, class A-code,
	// #857). Nil pins nothing; otherwise it names one CPU per loop, from 0 to
	// 8191, or Start refuses it with LoopValueInvalid. A CPU the kernel refuses —
	// offline, outside the process's set, or a seccomp filter — leaves that loop
	// unpinned, serving, and the row missing, a fault. With Shards(0) and
	// so-incoming-cpu opted into, each loop's listener asks the kernel for the
	// connections its CPU received. The portable backend ignores it.
	Affinity []int `json:"affinity,omitempty"`
	// NotsentLowat is TCP_NOTSENT_LOWAT, in bytes, on the group's TCP listeners,
	// inherited by each accepted socket (tcp-notsent-lowat, class A-code, Linux
	// 3.12, #857): the kernel takes at most that many unsent bytes, so a write
	// is short and OnWritable fires once the peer drained, not once megabytes
	// of socket buffer emptied. Zero sets nothing unless Capabilities opts into
	// tcp-notsent-lowat, which selects 128 KiB; a positive value opts in;
	// negative is refused with LoopValueInvalid. The portable backend ignores it.
	NotsentLowat int `json:"notsent_lowat"`
	// SlabBudget is the most payload memory, in bytes, one loop maps outside the
	// Go heap for its connections' spill and output together (#809): past it a
	// Write is refused with LoopOutputFull and a spill closes its connection
	// with LoopSpillFull. Zero derives it from the cgroup allowance the server
	// reads with memlimit, a quarter of the allowance shared by the loops and
	// at most 128 MiB, and is 128 MiB where no allowance is found; negative is
	// refused with LoopValueInvalid. A size class's wholly free arenas past the
	// first are unmapped on the loop's tick. The portable backend ignores it.
	SlabBudget int `json:"slab_budget"`
	// BusyPoll is busy-poll's window (class A-code, #979): how long a loop's wait
	// polls the device queues of its sockets before it sleeps, io_uring's
	// busy_poll_to and epoll's busy_poll_usecs, rounded up to whole
	// microseconds. Zero selects 50 µs once Capabilities opts into busy-poll; it
	// opts nothing in by itself. Negative, or past one second, is refused with
	// LoopValueInvalid. The portable backend ignores it.
	BusyPoll DurationValue `json:"busy_poll"`
	// BusyPollBudget is how many packets one poll of busy-poll may take, epoll's
	// busy_poll_budget: zero is the kernel's, and past 64 the kernel asks for
	// CAP_NET_ADMIN, its refusal reading the row missing. Negative, or past
	// 65535, is refused with LoopValueInvalid. io_uring always takes the
	// kernel's; the portable backend ignores it.
	BusyPollBudget int `json:"busy_poll_budget"`
	// ZCRX names the NIC receive queue uring-zcrx would serve (class B, #980). A
	// zero Interface keeps the row off. While the row is class B a named queue
	// is only probed — registered on a throwaway ring that is closed at once,
	// the kernel's answer in the row's reason — and no byte is received through
	// it.
	ZCRX ZCRXValue `json:"zcrx"`
}

// ZCRXValue names the network device queue io_uring's zero-copy receive registers
// (uring-zcrx, class B, ADR 0189 amended 2026-11-11): the interface, the receive
// queue its operator steered to the group's port, and the size of the area the
// payloads land in. Its zero value names none.
type ZCRXValue struct {
	// Interface is the network interface's name; empty names none.
	Interface string `json:"interface"`
	// Queue is the index of the interface's receive queue; negative is refused
	// with LoopValueInvalid.
	Queue int `json:"queue"`
	// AreaBytes is the size of the area the kernel lands payloads in, rounded up
	// to whole pages: zero selects 4 MiB, negative is refused with
	// LoopValueInvalid.
	AreaBytes int `json:"area_bytes"`
}
