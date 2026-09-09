// Package net — the shutdown signal a long-lived handler observes.
package net

import "context"

// drainSignalKey types the context key so no other package can collide with it
// and no string constant can be typo'd into a silent miss.
type drainSignalKey struct{}

// WithDrainSignal returns ctx carrying the channel a server closes when it
// begins draining.
//
// A request context is deliberately NOT what carries this. Cancelling it would
// tell every in-flight handler to abandon the response it is halfway through,
// which is the opposite of a graceful drain — the drain exists so those
// responses finish. This signal is additive: a handler that ignores it behaves
// exactly as before, and a handler that holds a connection open indefinitely —
// an event stream, a long poll — gets the one piece of information it cannot
// otherwise have, namely that finishing now is the cooperative thing to do.
//
// The channel is closed, never sent on, so every observer sees it and a late
// observer sees it immediately.
func WithDrainSignal(ctx context.Context, draining <-chan struct{}) context.Context {
	//: attach under a private key; the getter is the only reader.
	return context.WithValue(ctx, drainSignalKey{}, draining)
}

// DrainSignal returns the drain channel carried by ctx, or nil when the server
// serving this request publishes none.
//
// A nil channel is the right absence: receiving from it blocks forever, so a
// select that watches it alongside other cases simply never fires that case.
// A caller therefore needs no nil check.
func DrainSignal(ctx context.Context) <-chan struct{} {
	draining, carried := ctx.Value(drainSignalKey{}).(<-chan struct{})
	//: no signal published — a nil channel blocks forever, which is what an
	//: absent signal should do in a select.
	if !carried {
		//: nothing published this signal.
		return nil
	}
	//: the server's drain channel.
	return draining
}
