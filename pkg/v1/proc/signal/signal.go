//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/proc/signal .

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
// [Target] — via kill(2) on Unix. A positive [Target] is a pid; a [Target] below
// -1 addresses the process group whose id is its absolute value. Relay returns
// when the source channel closes (nil) or when a delivery fails (a typed
// RELAY_FAILED error). The reserved targets 0 and -1 are refused on every
// platform before anything is delivered.
//
// # Platform notes
//
// Parse, String, and Notify are portable. Relay has a native backend on Unix
// (kill(2)) and on Windows, which has no kill(2): a pid target is terminated
// with TerminateProcess, and a process-group target receives a console control
// event (CTRL_C for SIGINT, CTRL_BREAK otherwise). On the remaining platforms it
// returns the typed UNSUPPORTED_PLATFORM sentinel rather than acting, so code
// compiles and degrades gracefully everywhere.
package signal
