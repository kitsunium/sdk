// Package net — one listener's reported state.
package net

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
