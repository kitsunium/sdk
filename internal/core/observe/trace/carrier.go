// Package trace — the port a span context crosses a process boundary through.
// Reading and writing the W3C headers through it — Inject, Extract — is the
// engine's mechanism, internal/service/observe/trace (ADR 0160 §4).
package trace

// Carrier is the two-method surface a header set has to present for a span
// context to travel through it.
//
// It is exactly http.Header's Get/Set pair, on purpose and to the letter, so
// the engine's `trace.Inject(ctx, req.Header)` compiles with no adapter — and
// so this package still imports no net/http, which would drag an HTTP opinion
// into a contract that also has to serve a message queue and a gRPC metadata
// map.
//
// IFACE-PLUGIN: it is FROZEN at two methods (ADR 0039). Adding a third — Del,
// Values, a multi-value read — would break every downstream two-method double at
// compile time with no deprecation window, and every one of those additions is
// reachable as a sibling interface instead. Two is also what makes http.Header
// satisfy it structurally, which is the whole ergonomics.
type Carrier interface {
	Get(key string) string
	Set(key, value string)
}
