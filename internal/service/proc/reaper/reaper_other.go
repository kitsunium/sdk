//go:build !unix

// Package reaper — non-Unix no-op reaper: degrades cleanly where SIGCHLD and
// PR_SET_CHILD_SUBREAPER do not exist (e.g. Windows).
package reaper

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// noopReaper is the non-Unix coreproc.Reaper: every method is a no-op because
// the platform has no SIGCHLD-driven zombie problem to solve. One concrete
// reaper type per file.
type noopReaper struct{}

// New returns a no-op reaper on platforms without SIGCHLD/waitpid semantics. The
// options are accepted for signature parity but have no effect here.
func New(_ ...Option) coreproc.Reaper {
	//: no platform reaping facility exists — return the inert reaper.
	return noopReaper{}
}

// Start is a no-op on non-Unix platforms: there is no SIGCHLD loop to run.
func (noopReaper) Start() {
	//: nothing to start where the platform reaps its own children.
}

// Stop is a no-op on non-Unix platforms: there is no loop to tear down.
func (noopReaper) Stop() {
	//: nothing to stop on a platform with no background reaping loop.
}

// ReapOnce reports zero reaped and no error on non-Unix platforms, where there
// are no orphaned zombies to collect.
func (noopReaper) ReapOnce() (reaped int, err error) {
	//: no children to reap on this platform — a clean zero sweep.
	return 0, nil
}

// SetChildSubreaper reports UnsupportedPlatform on non-Unix platforms, which
// have no prctl(PR_SET_CHILD_SUBREAPER) equivalent.
func SetChildSubreaper() error {
	//: subreaper mode cannot be armed off Unix — return the central sentinel.
	return coreproc.UnsupportedPlatform
}

// IsPID1 reports false on non-Unix platforms, where the pid-1-is-init convention
// does not apply.
func IsPID1() bool {
	//: the init/PID1 convention is Unix-specific; report false elsewhere.
	return false
}
