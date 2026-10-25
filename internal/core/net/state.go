package net

// StateValue is a snapshot of the server's lifecycle and listeners.
//
// It is a value, so a caller can hold it, log it, or compare it without racing
// the server that produced it.
type StateValue struct {
	// Phase is where the server sits in its lifecycle.
	Phase Phase `json:"phase"`
	// Listeners describes every bound listener, including any degradation.
	Listeners []ListenerStateValue `json:"listeners"`
	// ActiveConns is the number of connections currently being served.
	ActiveConns int64 `json:"active_conns"`
	// TotalConns is the number accepted since the server started.
	TotalConns uint64 `json:"total_conns"`
	// RejectedConns is the number turned away by the connection ceiling.
	RejectedConns uint64 `json:"rejected_conns"`
	// OversizedPackets is the number of datagrams dropped for passing their
	// group's MaxPacketSize. They are counted rather than delivered because the
	// kernel has already discarded the tail, and the prefix that survives is
	// indistinguishable from a complete message to the handler receiving it. A
	// read loop has nobody to return an error to, so this counter is the report.
	OversizedPackets uint64 `json:"oversized_packets"`
	// AcceptBackoffs is the number of times an accept loop waited after Accept
	// failed with anything but its listener closing — the process out of file
	// descriptors, typically, with connections still queued. Retrying at once
	// would spin a core per listener while the condition lasts; each wait is
	// 5 ms, doubling to 1 s, and the next accepted connection starts the curve
	// over. A count that keeps rising is a server that cannot accept.
	AcceptBackoffs uint64 `json:"accept_backoffs"`
	// Loops reports every loop group (ADR 0186 §D8). It is empty while the
	// server serves none.
	Loops []LoopStateValue `json:"loops,omitempty"`
	// Override reports what KITSUNIUM_NET_ENGINE and KITSUNIUM_NET_DISABLE did
	// (ADR 0188).
	Override OverrideValue `json:"override"`
	// Faults lists everything the server runs without now, the degradations of
	// Listeners and Loops included; Degraded is true when it is not empty.
	Faults []FaultValue `json:"faults,omitempty"`
	// Fallbacks counts the engine fallbacks and keeps the last one.
	Fallbacks FallbacksValue `json:"fallbacks"`
	// Platform names the operating system, its release and the toolchain.
	Platform PlatformValue `json:"platform"`
}

// Degraded reports whether the server runs with any fault — a listener or loop
// group that fell back from a requested optimisation, a drained ring, a
// stalled loop group, an unknown environment token — so a caller can assert
// "no silent degradation" in one call. It reads Faults, and still the
// Degraded flags of Listeners and Loops for a value built by hand (ADR 0188).
func (s StateValue) Degraded() bool {
	//: a fault, whatever reported it, settles it.
	if len(s.Faults) > 0 {
		//: the server runs without something.
		return true
	}
	//: any degraded listener degrades the whole server's answer.
	for _, listener := range s.Listeners {
		//: the first degraded listener settles it.
		if listener.Degraded {
			//: one degraded listener settles the whole answer.
			return true
		}
	}
	//: a loop group that fell back from the backend it asked for degrades it
	//: the same way.
	for _, loop := range s.Loops {
		//: the first degraded loop group settles it.
		if loop.Degraded {
			//: one degraded loop group settles the whole answer.
			return true
		}
	}
	//: every listener and loop group got what it asked for.
	return false
}

// ListenerStateValue reports one bound listener.
//
// It carries the address actually bound rather than the one requested, and it
// reports any fallback explicitly, so a caller never has to infer either.
type ListenerStateValue struct {
	// Group is the listener group's name.
	Group string `json:"group"`
	// Address is the address actually bound, which for a port of zero is the
	// one the kernel chose rather than the one requested.
	Address string `json:"address"`
	// Network is the socket family.
	Network string `json:"network"`
	// Shards is how many listeners were actually opened on this address.
	Shards int `json:"shards"`
	// Degraded reports that a requested optimisation was unavailable and the
	// server fell back. It is surfaced rather than logged once at startup,
	// because a silent fallback is indistinguishable from a working one and
	// nobody re-reads yesterday's logs to find out.
	Degraded bool `json:"degraded"`
	// DegradedReason names the fallback in plain words when Degraded is set.
	DegradedReason string `json:"degraded_reason,omitempty"`
	// Adopted reports a listener inherited from a supervisor rather than bound
	// by this process.
	Adopted bool `json:"adopted"`
}
