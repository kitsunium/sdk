// Package proc — the Reaper port: collection of terminated child processes.
package proc

// Reaper collects terminated child processes so they do not linger as zombies —
// the core duty of any PID1 or subreaper. Implementations live in
// internal/service/proc/*; non-supporting platforms return a no-op reaper.
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
