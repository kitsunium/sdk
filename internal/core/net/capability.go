package net

// EnvEngine is the variable that caps every group's engine at one capability of
// the lattice: min(code, environment), never above the code's choice.
const EnvEngine string = "KITSUNIUM_NET_ENGINE"

// EnvDisable is the variable that switches engines off, a comma-separated list of
// capabilities the operator may switch off.
const EnvDisable string = "KITSUNIUM_NET_DISABLE"

// Capability names one engine of the lattice the operator's switches restrict
// (ADR 0188): iouring > epoll > portable > goroutine. Its values are the tokens
// KITSUNIUM_NET_ENGINE and KITSUNIUM_NET_DISABLE take; this block is the
// registry they are parsed against, and any other token is unknown.
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
