// Package signal — typed signal toolbox over os/signal: Notify subscribes to a
// set of signals on a leak-free channel, and Relay forwards received signals to
// a process or process group. The kill(2) path is platform-split into
// relay_unix.go / relay_other.go; this file holds the cross-platform Notify and
// the Target value, since os/signal itself is portable.
package signal

import (
	"os"
	ossignal "os/signal"
	"sync"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Target names the recipient of a relayed signal: a positive value is a pid; a
// value below -1 is the negation of a process-group id (kill(2) delivers to the
// whole group whose id is its absolute value). It is a plain int so call sites
// can pass a raw pid or -pgid without a constructor.
type Target int

// Notify subscribes to sigs and returns a receive-only channel of typed Signals
// plus a stop function. Each delivery of one of sigs is translated from its
// os.Signal carrier to a coreproc.Signal and sent on the channel. The channel is
// buffered (one slot per subscribed signal, minimum one) so a burst is not
// dropped while a reader is busy, matching the os/signal "never blocks the
// sender" contract.
//
// The returned stop function unregisters the os/signal channel (signal.Stop runs
// in the translation goroutine via defer), ends the goroutine, and closes the
// returned channel; it is idempotent and leak-free — after it returns no
// goroutine or signal registration survives. Calling it more than once is a safe
// no-op.
func Notify(sigs ...coreproc.Signal) (<-chan coreproc.Signal, func()) {
	//: one buffer slot per subscribed signal (floor of one) absorbs a burst
	//: without blocking the runtime's non-blocking send into the os channel.
	buf := max(len(sigs), 1)

	osCh := make(chan os.Signal, buf)
	out := make(chan coreproc.Signal, buf)
	//: drives both the goroutine exit and stop idempotency without a mutex.
	done := make(chan struct{})
	//: closed by the goroutine once registration is live, so Notify only returns
	//: after a signal can no longer be missed (no registration race window).
	ready := make(chan struct{})

	//: translate each requested signal to its os.Signal carrier for registration.
	osSigs := make([]os.Signal, 0, len(sigs))
	//: build the os.Signal slice os/signal.Notify expects.
	for _, s := range sigs {
		//: append the portable os.Signal carrier for this typed signal.
		osSigs = append(osSigs, s.OS())
	}

	//: the goroutine owns the os/signal registration lifecycle end to end:
	//: it registers, defers Stop, signals readiness, then translates until done.
	go translate(osCh, out, done, ready, osSigs)

	//: block until the goroutine has registered so an immediate signal is caught.
	<-ready

	//: sync.Once makes stop idempotent AND race-safe: concurrent or repeated
	//: teardown calls cannot double-close done (a plain bool guard would race
	//: and could panic on a second close under concurrency).
	var once sync.Once
	stop := func() {
		//: only the first caller closes done; every later call is a no-op.
		once.Do(func() {
			//: ending done makes the goroutine return; its defer runs signal.Stop
			//: and closes out for readers.
			close(done)
		})
	}

	//: hand back the receive-only view and the teardown handle.
	return out, stop
}

// translate registers osSigs on src, signals readiness via ready, then pumps
// os.Signal deliveries onto dst as typed Signals until done is closed. Its defer
// unregisters the os/signal subscription and closes dst so blocked readers
// observe the end of stream — owning the full registration lifecycle here keeps
// Notify's teardown a single channel close.
func translate(
	src chan os.Signal,
	dst chan<- coreproc.Signal,
	done <-chan struct{},
	ready chan<- struct{},
	osSigs []os.Signal,
) {
	//: register first so no signal is missed before the loop starts.
	ossignal.Notify(src, osSigs...)
	//: paired teardown: detach the subscription so the runtime stops sending.
	defer ossignal.Stop(src)
	//: closing dst on exit unblocks any reader waiting on the typed channel.
	defer close(dst)
	//: registration is live — let Notify return without a missed-signal race.
	close(ready)

	//: relay until the owner tears the subscription down via done.
	for {
		//: race a delivery against teardown; teardown always wins eventually.
		select {
		case s, ok := <-src:
			//: a closed src (never closed here) would otherwise hot-spin; guard it.
			if !ok {
				//: source closed unexpectedly — exit and run the defer.
				return
			}
			//: forward the typed value, but abandon the send if teardown fires.
			if !forward(dst, done, toSignal(s)) {
				//: teardown won the race during the send — exit and close dst.
				return
			}
		case <-done:
			//: teardown requested — stop translating and let the defer close dst.
			return
		}
	}
}

// toSignal re-keys an os.Signal carrier back to the typed Signal. os/signal only
// ever delivers syscall.Signal values; a non-syscall carrier collapses to the
// reserved invalid signal rather than panicking.
func toSignal(s os.Signal) coreproc.Signal {
	//: the runtime delivers syscall.Signal; convert it to the typed value.
	if sys, ok := s.(syscall.Signal); ok {
		//: zero-cost re-key from the platform signal number to Signal.
		return coreproc.Signal(sys)
	}
	//: an unexpected carrier maps to the reserved invalid signal, not a panic.
	return coreproc.Signal(0)
}

// forward sends sig on dst but yields to a concurrent teardown, reporting
// whether the value was delivered (false means teardown won the race).
func forward(dst chan<- coreproc.Signal, done <-chan struct{}, sig coreproc.Signal) bool {
	//: never block the translator: deliver the signal or honour teardown first.
	select {
	case dst <- sig:
		//: delivered to the reader's buffered channel.
		return true
	case <-done:
		//: teardown raced ahead of the send — drop this value and stop.
		return false
	}
}
