// Package net — the per-phase deadlines.
package net

// TimeoutsValue bounds each phase of a connection separately.
//
// Per-phase deadlines rather than one overall budget: a slow reader, a slow
// writer and an idle keep-alive connection are three different problems with
// three different right answers, and a single timeout cannot tell them apart.
//
// Every field is a PER-OPERATION bound, refreshed each time, not a budget for
// the connection's whole life. The distinction is the whole point: a peer
// sending one byte per second violates a per-read bound at once and stays
// inside a lifetime bound until the lifetime runs out.
type TimeoutsValue struct {
	// Read bounds one read from the peer.
	Read DurationValue `json:"read"`
	// Write bounds one write to the peer.
	Write DurationValue `json:"write"`
	// Idle bounds how long a connection may sit with no traffic at all.
	Idle DurationValue `json:"idle"`
	// Handshake bounds the TLS handshake, which is the phase a hostile peer can
	// most cheaply stall. Unlike the others, zero here does NOT mean unbounded:
	// the negotiation happens before any handler exists to notice it, so it
	// falls back to the domain's default instead.
	Handshake DurationValue `json:"handshake"`
}
