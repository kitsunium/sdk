//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/proc/reaper .

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
// [Reaper].Start installs an [os/signal] SIGCHLD handler and, on each signal,
// loops syscall.Wait4 with WNOHANG until it drains every reapable child. Start is
// idempotent. [Reaper].Stop performs one final drain and waits for the loop's
// goroutine to exit, so a Start/Stop cycle leaks neither zombies nor goroutines
// and may be repeated. [Reaper].ReapOnce runs a single non-blocking sweep and is
// safe to call concurrently with the loop — each child's exit is observed
// exactly once across whoever sweeps.
//
// # Children spawned through pkg/v1/proc/process
//
// A sweep collects EVERY exited child, including one a
// [github.com/kitsunium/sdk/pkg/v1/proc/process.Process] is waiting for. Its exit
// status is not lost: the sweep hands it to that Process, whose Wait reports
// the child's own exit code whichever of the two collected it. A child spawned
// any other way — os/exec, syscall.ForkExec, a C library — has no such claim, so
// a running reaper still takes its status and its own wait fails with ECHILD.
// Spawn what you wait for through pkg/v1/proc/process in a process that runs the
// reaper.
//
// ECHILD ("no children") is treated as a clean end of a sweep, not an error; any
// other wait error surfaces as the typed sentinel [ReapFailed].
//
// # Platform notes
//
// The real implementation is Unix-only. On platforms without SIGCHLD/waitpid
// (e.g. Windows), [New] returns a no-op reaper (Start/Stop do nothing, ReapOnce
// returns 0) and [SetChildSubreaper] returns [UnsupportedPlatform]. On illumos
// and Solaris the Go runtime forks every child with FORK_NOSIGCHLD, so the exit
// of a child this process spawned posts no SIGCHLD there; the loop also sweeps
// once a second on those two, which keeps "reap children as they exit" true. Code that
// links this package therefore builds and runs everywhere; only the behaviour
// degrades. [SetChildSubreaper] requires no privilege but is a per-process
// Linux capability; where the prctl is unavailable it returns a typed error
// rather than panicking.
package reaper
