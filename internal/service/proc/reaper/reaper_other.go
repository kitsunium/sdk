//go:build !unix

// Package reaper — non-Unix no-op reaper: degrades cleanly where SIGCHLD and
// PR_SET_CHILD_SUBREAPER do not exist (e.g. Windows).
package reaper

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// noopReaper is the non-Unix coreproc.Reaper: Start/Stop do nothing because the
// platform has no SIGCHLD-driven zombie problem, but ReapOnce still honours the
// WithOnReap observer so the post-sweep hook contract is identical on every
// platform — it simply reports a zero-child sweep. One concrete reaper per file.
type noopReaper struct {
	// onReap mirrors config.onReap: an optional post-sweep observer. It is held
	// here, rather than ignored, so the WithOnReap contract ("invoked after every
	// sweep, including zero") holds on non-Unix platforms too.
	onReap func(int)
}

// New returns a no-op reaper on platforms without SIGCHLD/waitpid semantics. The
// WithOnReap observer is still retained so its cross-platform contract holds;
// only the actual reaping degrades to a zero-child sweep here.
func New(opts ...Option) coreproc.Reaper {
	//: fold the options so the no-op reaper still carries the onReap observer.
	cfg := resolve(opts)
	//: no platform reaping facility exists — return the inert reaper that still
	//: fires the observer on ReapOnce for a consistent cross-platform contract.
	return noopReaper{onReap: cfg.onReap}
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
// are no orphaned zombies to collect. It still fires the WithOnReap observer
// with the zero count so the post-sweep hook contract matches Unix.
func (r noopReaper) ReapOnce() (reaped int, err error) {
	//: honour the observer contract: a sweep reaped zero children here.
	if r.onReap != nil {
		//: report the zero-child sweep so the hook fires on every platform.
		r.onReap(0)
	}
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
