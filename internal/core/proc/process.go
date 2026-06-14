// Package proc — the Process port: lifetime control over a spawned process.
package proc

import (
	"context"
	"time"
)

// Process is a handle to a spawned process. It exposes only lifetime control —
// observe completion, deliver a signal to the leader or its whole group, and
// stop it gracefully. Implementations live in internal/service/proc/*; the
// pkg/v1/process facade returns this interface.
type Process interface {
	// PID reports the process identifier of the leader.
	PID() int
	// Wait blocks until the process exits and returns its ExitValue. It is safe
	// to call once; concurrent or repeated calls observe the same outcome. Under
	// StdioCapture it returns only after every captured byte has reached the
	// caller's writers; if a writer itself failed, the ExitValue still reports the
	// real exit status and the error is StdioCaptureFailed.
	Wait() (ExitValue, error)
	// Signal delivers sig to the leader process only.
	Signal(sig Signal) error
	// SignalGroup delivers sig to the leader's entire process group (kill(-pgid,
	// sig)), reaching forked grandchildren — the KillMode=control-group analogue.
	SignalGroup(sig Signal) error
	// Stop terminates the process group gracefully: it sends sig, waits up to
	// grace for exit, then escalates to SIGKILL. It returns when the process has
	// exited or ctx is cancelled.
	Stop(ctx context.Context, grace time.Duration, sig Signal) error
}
