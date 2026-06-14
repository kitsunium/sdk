//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/reaper .

// Package reaper is the PID1 / subreaper zombie-collector facade.
//
// A process that becomes the parent of orphaned descendants — an init (pid 1)
// in a container, or any supervisor that calls [SetChildSubreaper] — must reap
// the children that reparent to it, or they accumulate as zombies that exhaust
// the system's pid space. This package wraps the kernel mechanics (a SIGCHLD
// handler driving a non-blocking waitpid loop, plus prctl(PR_SET_CHILD_SUBREAPER))
// behind a tiny, portable surface.
//
//	r := reaper.New()
//	r.Start()          // background: reap children as they exit
//	defer r.Stop()     // final drain; no zombie outlives shutdown
//
//	// A non-init supervisor opts in to collecting orphaned grandchildren:
//	if !reaper.IsPID1() {
//		if err := reaper.SetChildSubreaper(); err != nil {
//			// degrade: orphans will reparent to pid 1 instead
//		}
//	}
//
// # Semantics
//
// [Reaper.Start] installs an [os/signal] SIGCHLD handler and, on each signal,
// loops syscall.Wait4 with WNOHANG until it drains every reapable child. Start is
// idempotent. [Reaper.Stop] performs one final drain and waits for the loop's
// goroutine to exit, so a Start/Stop cycle leaks neither zombies nor goroutines
// and may be repeated. [Reaper.ReapOnce] runs a single non-blocking sweep and is
// safe to call concurrently with the loop — Wait4 is kernel-serialised, so each
// child's exit is observed exactly once across whoever sweeps.
//
// ECHILD ("no children") is treated as a clean end of a sweep, not an error; any
// other wait error surfaces as the typed sentinel [ReapFailed].
//
// # Platform notes
//
// The real implementation is Unix-only. On platforms without SIGCHLD/waitpid
// (e.g. Windows), [New] returns a no-op reaper (Start/Stop do nothing, ReapOnce
// returns 0) and [SetChildSubreaper] returns [UnsupportedPlatform]. Code that
// links this package therefore builds and runs everywhere; only the behaviour
// degrades. [SetChildSubreaper] requires no privilege but is a per-process
// Linux capability; where the prctl is unavailable it returns a typed error
// rather than panicking.
package reaper

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svc "github.com/kitsunium/sdk/internal/service/proc/reaper"
)

// Reaper collects terminated child processes so they do not linger as zombies —
// the core duty of any PID1 or subreaper. It is an alias of the core port; the
// concrete value is platform-selected by [New].
type Reaper = coreproc.Reaper

// Option configures a [Reaper] returned by [New]. It is an alias of the service
// option type so callers compose options without importing internal packages.
type Option = svc.Option

// The exported sentinels re-export the central proc error vars so callers match
// reaper failures without importing internal packages. Match with errs.HasCode
// and [github.com/kitsunium/sdk/pkg/v1/errs].
var (
	// ReapFailed is returned by a sweep when wait4 fails with an error other
	// than the benign ECHILD ("no children") condition.
	ReapFailed = coreproc.ReapFailed
	// SubreaperFailed is returned by [SetChildSubreaper] when
	// prctl(PR_SET_CHILD_SUBREAPER) fails to arm subreaper mode.
	SubreaperFailed = coreproc.SubreaperFailed
	// UnsupportedPlatform is returned by [SetChildSubreaper] on platforms
	// without a prctl(PR_SET_CHILD_SUBREAPER) equivalent (e.g. Windows).
	UnsupportedPlatform = coreproc.UnsupportedPlatform
)

// WithOnReap registers fn as a post-sweep observer receiving the number of
// children reaped in each sweep (including zero). fn must not block.
func WithOnReap(fn func(int)) Option {
	//: delegate to the service option constructor.
	return svc.WithOnReap(fn)
}

// New returns a [Reaper] configured by opts. On Unix it is a SIGCHLD-driven
// waitpid loop; elsewhere it is a no-op. The reaper starts idle — call Start.
func New(opts ...Option) Reaper {
	//: delegate construction to the platform-selected service implementation.
	return svc.New(opts...)
}

// SetChildSubreaper marks the calling process as a child subreaper so orphaned
// descendants reparent to it rather than to PID1. It returns [SubreaperFailed]
// on a prctl error and [UnsupportedPlatform] off Unix.
func SetChildSubreaper() error {
	//: delegate to the platform-selected service implementation.
	return svc.SetChildSubreaper()
}

// IsPID1 reports whether the current process is the init process (pid 1). It is
// false on platforms where the convention does not apply.
func IsPID1() bool {
	//: delegate to the platform-selected service implementation.
	return svc.IsPID1()
}
