package kit

import (
	"context"
	"time"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Loop is a declared loop of the daemon: kit owns the wait, the product owns
// the work. Its wake sources — a period, a deadline the product computes, a
// topic — are data, so the diagram draws exactly what wakes it; kit runs it on
// its own goroutine, one run at a time, observes every run, and restarts it
// after a panic.
type Loop = ikit.Loop

// Wake says why a declared loop runs: the reason, the instant, and the
// topic that woke it when one did.
type Wake = ikit.WakeEvent

// LoopOption configures a declared loop.
type LoopOption = ikit.LoopConfigurer

// Routine is a loop written by hand: a function that runs until its context
// ends. kit starts it with the app, cancels its context on shutdown and waits
// for it, restarts it after an error or a panic — backing off from one second
// to one minute — and reads its select statements to draw what it waits on.
type Routine = ikit.Routine

// wakeEvery is WakeEvery's body: decl_gen.go writes WakeEvery, from the
// design, as one call of it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func wakeEvery(d time.Duration) LoopOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.WakeEvery(d)
}

// wakeAt is WakeAt's body: decl_gen.go writes WakeAt, from the
// design, as one call of it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func wakeAt(next func(context.Context) (time.Time, bool)) LoopOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.WakeAt(next)
}

// WakeOn wakes the loop whenever a message is published on topic. It is a
// nudge, not a subscription: the loop is not handed the message and wakes
// once for a burst — it re-reads the state it works on.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func WakeOn[T any](topic *ikit.TopicService[T]) LoopOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.WakeOn[T](topic)
}
