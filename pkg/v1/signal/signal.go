//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/signal .

// Package signal is the typed signal toolbox: parse, subscribe, and forward OS
// signals through one ergonomic surface.
//
// It is a thin facade over the SDK process-supervision domain. Signals are typed
// values ([Signal]) that round-trip through their canonical name, [Notify]
// subscribes to a set of them on a leak-free channel, and [Relay] forwards a
// stream of received signals to another process or process group.
//
//	// Translate a graceful-shutdown request and act on it.
//	term, _ := signal.Parse("SIGTERM")         // typed value
//	intr, _ := signal.Parse("SIGINT")
//	ch, stop := signal.Notify(term, intr)
//	defer stop()                               // leak-free teardown
//	<-ch                                       // block until a signal arrives
//
//	// Forward every signal we receive down to a child process group.
//	src, stop := signal.Notify(term)
//	defer stop()
//	go signal.Relay(src, signal.Target(-childPGID)) // negative ⇒ process group
//
// # Notify semantics
//
// [Notify] registers an os/signal subscription and runs a translation goroutine
// that converts each delivery to a typed [Signal]. The returned channel is
// buffered (one slot per subscribed signal, floor of one) so a burst is not
// dropped while the reader is busy. The returned stop function detaches the
// subscription, ends the goroutine, and closes the channel; it is idempotent and
// leaves no goroutine or registration behind.
//
// # Relay semantics
//
// [Relay] reads from the source channel and delivers each signal to the
// [Target] via kill(2). A positive [Target] is a pid; a [Target] below -1
// addresses the process group whose id is its absolute value. Relay returns when
// the source channel closes (nil) or when a delivery fails (a typed
// RELAY_FAILED error).
//
// # Platform notes
//
// Parse, String, and Notify are portable. Relay requires kill(2): on non-Unix
// platforms it returns the typed UNSUPPORTED_PLATFORM sentinel rather than
// acting, so code compiles and degrades gracefully everywhere.
package signal

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcsignal "github.com/kitsunium/sdk/internal/service/proc/signal"
)

// Signal is a typed, platform-portable OS signal. It aliases the domain type, so
// a value parsed here interoperates with the rest of the SDK process domain.
type Signal = coreproc.Signal

// Target names the recipient of a relayed signal: a positive value is a pid; a
// value below -1 addresses the process group whose id is its absolute value.
type Target = svcsignal.Target

// Parse resolves a signal from its name or number — the canonical "SIGTERM", the
// bare "TERM", or the numeric "15", all case-insensitive — returning a typed
// UNKNOWN_SIGNAL error for anything the platform table does not define.
func Parse(name string) (sig Signal, err error) {
	//: delegate to the domain parser; this facade adds no parsing of its own.
	return coreproc.Parse(name)
}

// Notify subscribes to sigs and returns a receive-only channel of typed Signals
// plus an idempotent, leak-free stop function. Each delivery is translated to a
// typed Signal; the channel is buffered so a burst is not dropped, and stop
// detaches the subscription and closes the channel.
func Notify(sigs ...Signal) (ch <-chan Signal, stop func()) {
	//: delegate to the service implementation that owns the os/signal lifecycle.
	return svcsignal.Notify(sigs...)
}

// Relay reads signals from src and forwards each to target via kill(2) until src
// closes. A positive target is a pid; a target below -1 addresses the process
// group whose id is -target. It returns nil on a clean drain and a typed
// RELAY_FAILED error on the first delivery failure (UNSUPPORTED_PLATFORM off
// Unix).
func Relay(src <-chan Signal, target Target) error {
	//: delegate to the platform-split service Relay (kill(2) on Unix, stub else).
	return svcsignal.Relay(src, target)
}
