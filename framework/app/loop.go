package app

import (
	"context"
	"time"

	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// Loop is a declared loop of the daemon: the framework owns the wait, the product owns
// the work. Its wake sources — a period, a deadline the product computes, a
// topic — are data, so the diagram draws exactly what wakes it; the framework runs it on
// its own goroutine, one run at a time, observes every run, and restarts it
// after a panic.
type Loop = iapp.Loop

// Wake says why a declared loop runs: the reason, the instant, and the
// topic that woke it when one did. Wakes that come while it runs, or while
// it backs off after a failure, are gathered into one run: Reasons and
// Topics say every one of them, and Reason the one that decides the run.
type Wake = iapp.WakeEvent

// LoopOption configures a declared loop.
type LoopOption = iapp.LoopConfigurer

// Routine is a loop written by hand: a function that runs until its context
// ends. The framework starts it with the app, cancels its context on shutdown and waits
// for it, restarts it after an error or a panic — backing off from one second
// to one minute — and reads its select statements to draw what it waits on.
type Routine = iapp.Routine

// WakeEvery wakes the loop every d.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func WakeEvery(d time.Duration) LoopOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.WakeEvery(d)
}

// WakeAt wakes the loop at the time next returns, asked again after every
// run; false means "no deadline for now". It is how a loop sleeps exactly
// until its next piece of work — the next reminder due — rather than polling.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func WakeAt(next func(context.Context) (time.Time, bool)) LoopOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.WakeAt(next)
}

// WakeOn wakes the loop whenever a message is published on topic. It is a
// nudge, not a subscription: the loop is not handed the message and wakes
// once for a burst — it re-reads the state it works on.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func WakeOn[T any](topic *iapp.TopicService[T]) LoopOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.WakeOn[T](topic)
}

// RoutineOption configures a hand-written loop ([Service].Go).
type RoutineOption = iapp.RoutineConfigurer

// SupervisionOption configures what the framework restarts after a
// failure: a subscription's, a watch's or a queued command's consumer, a
// declared loop, a hand-written loop ([GiveUpAfter]).
type SupervisionOption = iapp.SupervisionConfigurer

// GiveUpAfter bounds how often the framework restarts what fails — a
// subscription's, a watch's or a queued command's consumer whose queue
// failed, a hand-written loop ([Service].Go) that returned, a declared
// loop ([Service].Loop) whose run failed or panicked —: once it failed
// more than restarts times within within, it is not restarted again, and
// the product is no longer live — its liveness check
// "supervised:<node>" fails, the problem names it — so that whatever
// watches its liveness — an orchestrator's probe — restarts the process; a
// product nothing probes keeps running without it, and says so in its log,
// its problems and its telemetry. It is Erlang's restart
// intensity, the SDK's lifecycle supervisor's GiveUpAfter.
//
// The default is 10 within 10 minutes: with the restarts' backoff — one
// second, doubling up to a minute — a component that fails at every start
// is given up about five minutes after its first failure, and one that
// fails once a minute or less often is restarted forever. A within of zero
// never gives up; a negative value stops the start.
//
// restarts and within are the defaults of the settings
// <service>-<kind>-<name>-give-up-restarts and -give-up-within, the kind
// subscription, command or loop, which config/<env>.* and <PRODUCT>_<NAME>
// override.
//
// IFACE-OPAQUE: the option is sealed — its methods are unexported — so a
// caller only hands it to the declaration it configures.
func GiveUpAfter(restarts int, within time.Duration) SupervisionOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.GiveUpAfter(restarts, within)
}
