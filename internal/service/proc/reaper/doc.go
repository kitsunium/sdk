// Package reaper — the PID1 / subreaper zombie collector backing pkg/v1/proc/reaper.
//
// New returns a coreproc.Reaper whose concrete behaviour is selected at build
// time: a real SIGCHLD-driven waitpid loop on Unix (reaper_unix.go) and a
// degrade-to-no-op stub elsewhere (reaper_other.go). SetChildSubreaper and
// IsPID1 are likewise platform-split. This file holds only the cross-platform
// Option surface so the option type is declared once.
//
// Package reaper — non-Unix no-op reaper: degrades cleanly where SIGCHLD and
// PR_SET_CHILD_SUBREAPER do not exist (e.g. Windows).
//
// Package reaper — Unix SIGCHLD waitpid reaping loop (all Unix). The
// PR_SET_CHILD_SUBREAPER arming lives in the Linux-only sibling.
//
// Package reaper — FreeBSD / DragonFly BSD descendant-reaper arming via
// procctl(2). procctl(P_PID, 0, PROC_REAP_ACQUIRE, NULL) makes the calling
// process the reaper for its whole descendant tree, the BSD analogue of Linux's
// prctl(PR_SET_CHILD_SUBREAPER): orphaned descendants reparent to this process
// instead of PID1, so a non-init supervisor still receives their SIGCHLD.
//
// Package reaper — DragonFly BSD procctl(2) ABI constants for SetChildSubreaper.
//
// Package reaper — FreeBSD procctl(2) ABI constants for SetChildSubreaper.
//
// Package reaper — Linux PR_SET_CHILD_SUBREAPER arming via prctl(2).
//
// Package reaper — non-Linux Unix subreaper stub for platforms with no
// reparent-here facility (darwin/openbsd/netbsd/solaris): neither Linux's
// prctl(PR_SET_CHILD_SUBREAPER) nor BSD's procctl(PROC_REAP_ACQUIRE) exists, so
// arming degrades to a typed error. FreeBSD and DragonFly are handled by the
// real procctl(2) sibling (subreaper_bsd.go).
//
// Package reaper — the timer sweep illumos and Solaris need beside SIGCHLD (the
// solaris build tag selects both).
//
// Package reaper — no timer sweep where every child's exit posts SIGCHLD.
package reaper
