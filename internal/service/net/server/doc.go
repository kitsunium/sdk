// Package server — adoption of listeners inherited from a supervisor.
//
// Package server — the datagram batch-read abstraction.
//
// Package server — compile-time interface assertions, kept out of the
// production source per KTN-IFACE-ASSERT-PLACEMENT.
//
// Package server — the portable datagram reader.
//
// Package server — the pooled stream connection.
//
// Package server — compile-time interface assertions, kept out of the
// production source per KTN-IFACE-ASSERT-PLACEMENT.
//
// Package server — the socket that holds its connection slot until it closes.
//
// Package server — the completion state of one connection handed to net/http.
//
// Package server — batch-reader selection on Linux.
//
// Package server — batch-reader selection on platforms without recvmmsg.
//
// Package server — every served socket family is left to the kernel here.
//
// Package server — the two socket families Windows cannot open at all.
//
// Package server — the bounded TLS handshake.
//
// Package server — the net/http adapter.
//
// Package server — the bounds only an HTTP group has.
//
// Package server — the listener bridge that lets net/http consume our accepts.
//
// Package server — the width-correct kernel length assignment.
//
// Package server — the start, accept and drain lifecycle.
//
// Package server — compile-time interface assertions, kept out of the
// production source per KTN-IFACE-ASSERT-PLACEMENT.
//
// Package server — listener construction.
//
// Package server — the listener that hands out close-tracking sockets.
//
// Package server — the recvmmsg batched datagram reader.
//
// Package server — the recvmmsg message header layout.
//
// Package server — the functional options.
//
// Package server — the pooled datagram.
//
// Package server — a group of datagram listeners.
//
// Package server — datagram listener construction and the read loop.
//
// Package server — the datagram read loop.
//
// Package server — per-connection state recycling.
//
// Package server — the descriptor-bearing socket contract.
//
// Package server — SO_REUSEPORT support on the BSD family.
//
// Package server — SO_REUSEPORT support on Linux.
//
// Package server — the SO_REUSEPORT floor for platforms without it.
//
// Package server is the inbound half of the SDK's network domain (ADR 0029):
// one unified listener engine for TCP, Unix, TLS and mutual TLS, serving
// pluggable handlers grouped behind shared middlewares and policies.
//
// Package server — kernel sockaddr decoding for the batched reader.
//
// Package server — a group of listeners sharing one handler and policy.
//
// Package server — the per-group connection ceiling.
//
// Package server — deliberate discard of non-actionable cleanup errors.
//
// Package server — how a Unix kernel reports a datagram longer than the read
// buffer: by truncating it, which is not an error at all.
//
// Package server — how Windows reports a datagram longer than the read buffer.
package server
