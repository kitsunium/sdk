// Package process — ergonomic re-exports: the handful of signal constants and
// resource sentinels callers need to drive Stop/SignalGroup and read typed
// errors without importing internal/core/proc directly.
package process

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
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

// The resource sentinels callers most often set in Spec.Rlimits, re-exported so
// a Spec can be built without importing internal/core/proc. The full set lives
// on the core Resource type.
const (
	// ResourceNoFile limits the highest open file descriptor (RLIMIT_NOFILE).
	ResourceNoFile Resource = coreproc.ResourceNoFile
	// ResourceCore limits the size of a core dump in bytes (RLIMIT_CORE).
	ResourceCore Resource = coreproc.ResourceCore
	// ResourceCPU limits CPU time in seconds (RLIMIT_CPU).
	ResourceCPU Resource = coreproc.ResourceCPU
	// ResourceAS limits the process virtual address-space size (RLIMIT_AS).
	ResourceAS Resource = coreproc.ResourceAS
)
