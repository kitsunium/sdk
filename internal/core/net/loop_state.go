package net

// LoopRingStateValue reports one ring of one loop: counters the loop refreshes
// at the end of each batch, read without stopping it (ADR 0186 §D8).
type LoopRingStateValue struct {
	// Loop is the loop's index in its group.
	Loop int `json:"loop"`
	// Ring is the ring's index in its loop.
	Ring int `json:"ring"`
	// Conns is the number of connections the ring holds now.
	Conns int64 `json:"conns"`
	// Accepted is the number of connections the ring accepted.
	Accepted uint64 `json:"accepted"`
	// Closed is the number of connections the ring closed.
	Closed uint64 `json:"closed"`
	// Batches is the number of loop iterations that reaped this ring.
	Batches uint64 `json:"batches"`
	// Completions is the number of completions or events the ring reaped.
	Completions uint64 `json:"completions"`
	// BufferStarved is the number of receives that found no provided buffer
	// (ENOBUFS) and waited for one to come back.
	BufferStarved uint64 `json:"buffer_starved"`
	// EnterRetries is the number of times submitting to the kernel was retried
	// after it was busy — the completion queue full, typically.
	EnterRetries uint64 `json:"enter_retries"`
	// AcceptPauses is the number of times the ring stopped accepting: the
	// descriptor reserve reached, or the process out of descriptors.
	AcceptPauses uint64 `json:"accept_pauses"`
	// Evicted is the number of idle connections closed to admit others.
	Evicted uint64 `json:"evicted"`
	// WatchdogFires is the number of times the liveness watchdog saw a stall it
	// could observe and acted on it.
	WatchdogFires uint64 `json:"watchdog_fires"`
	// Draining reports a ring the watchdog judged sick: it accepts nothing new
	// and closes its connections as they fall idle.
	Draining bool `json:"draining"`
}

// LoopStateValue reports one loop group: the backend it runs on, how it got
// there, how it is sized, and its rings' counters (ADR 0186 §D8). A fallback is
// reported here and by Degraded, never silently.
type LoopStateValue struct {
	// Group is the loop group's name.
	Group string `json:"group"`
	// Backend is the backend in use.
	Backend LoopBackend `json:"backend"`
	// ProbeStage names the last stage of the kernel probe the backend passed,
	// or the stage that failed when the group fell back.
	ProbeStage string `json:"probe_stage"`
	// Degraded reports that the group runs on a backend other than the best one
	// it asked for: auto fell back from io_uring, say.
	Degraded bool `json:"degraded"`
	// DegradedReason names the fallback and its cause when Degraded is set.
	DegradedReason string `json:"degraded_reason,omitempty"`
	// Loops is the number of loops running.
	Loops int `json:"loops"`
	// Rings is the number of rings per loop.
	Rings int `json:"rings"`
	// RingsSource says where Rings came from: "explicit", "derived" from the
	// connection bounds and the descriptor limit, or "single" when no bound was
	// set or the backend runs one ring.
	RingsSource string `json:"rings_source"`
	// RLimitNoFile is the effective RLIMIT_NOFILE soft limit read at Start. The
	// SDK never raises it; the Go runtime already lifts the soft limit to the
	// hard one.
	RLimitNoFile uint64 `json:"rlimit_nofile"`
	// GOMAXPROCS is runtime.GOMAXPROCS(0) read at Start. The SDK never sets it.
	GOMAXPROCS int `json:"gomaxprocs"`
	// BatchP99 is the 99th percentile of one loop iteration's duration, over the
	// loops' recent batches.
	BatchP99 DurationValue `json:"batch_p99"`
	// OutboundRefused counts the jobs a full queue to a worker refused
	// (LoopOutboundFull), every lane's: for the http1 adapter, the 503s it
	// answered for them. Zero when the group offloads nothing.
	OutboundRefused uint64 `json:"outbound_refused"`
	// RingStates reports every ring of every loop, loop by loop.
	RingStates []LoopRingStateValue `json:"ring_states"`
	// Capabilities reports every optional kernel capability the group's backend
	// probed or was told not to, the server's socket probes merged in
	// (ADR 0189).
	Capabilities []CapabilityValue `json:"capabilities,omitempty"`
}
