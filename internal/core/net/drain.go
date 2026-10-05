package net

import "context"

// drainSignalKey types the context key so no other package can collide with it
// and no string constant can be typo'd into a silent miss.
type drainSignalKey struct{}

// withDrainSignal is WithDrainSignal's body: decl_gen.go writes WithDrainSignal, from the
// design, as one call of it.
func withDrainSignal(ctx context.Context, draining <-chan struct{}) context.Context {
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
