package service

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// The signals the engine names, as every platform spells them.
const (
	// sigTERM is KillSignal='s default off Windows.
	sigTERM = coreproc.Signal(syscall.SIGTERM)
	// sigKILL is FinalKillSignal='s default, and Windows' only signal.
	sigKILL = coreproc.Signal(syscall.SIGKILL)
	// sigABRT is WatchdogSignal='s default off Windows.
	sigABRT = coreproc.Signal(syscall.SIGABRT)
	// sigHUP is ReloadSignal='s default.
	sigHUP = coreproc.Signal(syscall.SIGHUP)
	// sigINT is a clean end for a daemon.
	sigINT = coreproc.Signal(syscall.SIGINT)
	// sigPIPE is a clean end for a daemon.
	sigPIPE = coreproc.Signal(syscall.SIGPIPE)
)
