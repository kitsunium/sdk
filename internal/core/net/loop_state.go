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
	// BatchesCut is the number of batches BatchBudget stopped while this ring
	// still had completions to reap (ADR 0206).
	BatchesCut uint64 `json:"batches_cut"`
	// BytesIn is the number of bytes the ring received and delivered to the handler.
	BytesIn uint64 `json:"bytes_in"`
	// BytesOut is the number of bytes the ring sent: the answers the kernel took, a short
	// send counted as it left.
	BytesOut uint64 `json:"bytes_out"`
	// OutputRefused is the number of Writes the ring refused with LoopOutputFull: the
	// connection's output ceiling, or the slab's budget, reached.
	OutputRefused uint64 `json:"output_refused"`
	// SpillClosed is the number of connections the ring closed with LoopSpillFull:
	// unconsumed input past SpillCap, or the slab's budget spent.
	SpillClosed uint64 `json:"spill_closed"`
	// Timeouts is the number of deadlines of SetDeadline that fired (OnTimeout calls).
	Timeouts uint64 `json:"timeouts"`
	// IdleClosed is the number of connections the ring closed for IdleTimeout.
	IdleClosed uint64 `json:"idle_closed"`
	// PendingOutput is the bytes written and not yet sent, a gauge: what the ring's
	// connections still owe their peers at the last batch's end.
	PendingOutput int64 `json:"pending_output"`
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
	// LoopsSource says where Loops came from: "explicit", "auto" — LoopsAuto,
	// GOMAXPROCS read at Start — or "single" when Loops was zero.
	LoopsSource string `json:"loops_source"`
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
	// SlabInUse is the bytes of the spill and output chunks every loop's slab holds at
	// the last batches' end, chunk sizes counted, a gauge.
	SlabInUse int64 `json:"slab_in_use"`
	// SlabMapped is the bytes every loop's slab has mapped outside the Go heap. A size
	// class keeps one wholly free arena and unmaps the others on the loop's tick
	// (#809), so it falls back after a burst.
	SlabMapped int64 `json:"slab_mapped"`
	// SlabBudget is the ceiling of SlabInUse, every loop's summed: past it a Write is
	// refused and a spill closes its connection, by name, never by the kernel's OOM
	// killer.
	SlabBudget int64 `json:"slab_budget"`
	// SlabBudgetSource says where each loop's slab budget came from (#809):
	// "explicit" for LoopValue.SlabBudget, "derived" from the cgroup allowance
	// memlimit read, "default" for 128 MiB a loop; empty on the portable
	// backend, which has no slab.
	SlabBudgetSource string `json:"slab_budget_source"`
	// RingStates reports every ring of every loop, loop by loop.
	RingStates []LoopRingStateValue `json:"ring_states"`
	// Capabilities reports every optional kernel capability the group's backend
	// probed or was told not to, the server's socket probes merged in
	// (ADR 0189).
	Capabilities []CapabilityValue `json:"capabilities,omitempty"`
}
