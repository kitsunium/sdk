// Package process — the running process itself: what it was built from and
// what it is doing.
//
// Package process — ergonomic re-exports: the handful of signal constants and
// resource sentinels callers need to drive Stop/SignalGroup and read typed
// errors without importing internal/core/proc directly.
//
// Package process — StdioMode re-exports for wiring a child's standard streams.
package process

import (
	"syscall"
)

// The common control signals as typed process.Signal values, so callers pass
// process.SIGTERM to Stop/Signal without a syscall import. They equal any
// Signal parsed from the same name (the underlying value is the platform number).
const (
	// SIGTERM is the polite termination request — the default Stop signal.
	SIGTERM = Signal(syscall.SIGTERM)
	// SIGKILL is the unignorable kill Stop escalates to after the grace window.
	SIGKILL = Signal(syscall.SIGKILL)
	// SIGINT is the interrupt signal (Ctrl-C analogue).
	SIGINT = Signal(syscall.SIGINT)
	// SIGHUP is the hang-up / reload signal many daemons treat as "reconfigure".
	SIGHUP = Signal(syscall.SIGHUP)
	// SIGQUIT is the quit-with-core signal.
	SIGQUIT = Signal(syscall.SIGQUIT)
)
