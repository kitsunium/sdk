// Package net — the server's reported state.
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
}

// Degraded reports whether any listener fell back from a requested
// optimisation, so a caller can assert "no silent degradation" in one call
// instead of walking the slice.
func (s StateValue) Degraded() bool {
	//: any degraded listener degrades the whole server's answer.
	for _, listener := range s.Listeners {
		//: the first degraded listener settles it.
		if listener.Degraded {
			//: one degraded listener settles the whole answer.
			return true
		}
	}
	//: every listener got what it asked for.
	return false
}
