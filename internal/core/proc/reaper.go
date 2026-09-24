// Package proc — the Reaper port: collection of terminated child processes.
package proc

// Reaper collects terminated child processes so they do not linger as zombies —
// the core duty of any PID1 or subreaper. Implementations live in
// internal/service/proc/*; non-supporting platforms return a no-op reaper.
//
// A sweep collects every exited child, including one a Process handle is
// waiting for; an implementation must hand such a child's exit status to that
// Process rather than discard it, so its Wait still reports the real exit.
type Reaper interface {
	// Start begins a background SIGCHLD-driven loop that reaps children as they
	// exit. It is idempotent: a second Start while running is a no-op.
	Start()
	// Stop ends the background loop after a final draining sweep, so shutdown
	// leaves no un-reaped zombies. It is safe to call without a prior Start.
	Stop()
	// ReapOnce performs a single non-blocking sweep and reports how many
	// children were reaped. It is safe to call concurrently with the loop.
	ReapOnce() (int, error)
}
