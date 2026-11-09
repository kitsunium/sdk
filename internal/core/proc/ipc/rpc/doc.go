// Package rpc is the contract of a call between two processes over the private
// socket (ADR 0218): the Caller a client is, the Handler that answers one
// method, the Router that names it, the Request a handler reads, the Stream a
// caller reads a server's items from, and the codes every refusal carries —
// range 0.3.106.*, declared here at the engine's path (ADR 0160).
//
// The engine is internal/service/proc/ipc/rpc: frames bounded before they are
// read, a fixed number of calls in flight per connection, deadlines on an
// injected clock, and a cancel frame for an abandoned call. Nothing here opens
// a socket or reads a frame. The ports exist so a handler is written, and
// tested, without one.
//
// Package rpc — the sentinels the engine refuses with. Each var's name equals
// its Reason in SCREAMING_SNAKE form; none quotes a method's body in its Public
// or Private half.
package rpc
