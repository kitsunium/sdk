//go:build solaris

// Package reaper — the timer sweep illumos and Solaris need beside SIGCHLD (the
// solaris build tag selects both).
package reaper

import "time"

// timerSweepEvery is how often the loop sweeps without waiting for a SIGCHLD.
//
// On illumos and Solaris the Go runtime forks every child with
// forkx(FORK_NOSIGCHLD) (go1.27.1 src/syscall/exec_libc.go), so the kernel
// posts no SIGCHLD when a child the process itself spawned exits — through
// pkg/v1/proc/process, os/exec or syscall.ForkExec alike. A loop woken by SIGCHLD
// alone would never collect such a child: its zombie would wait for its own
// Process.Wait, another child's signal, ReapOnce or Stop. Orphans re-parented
// here still signal: the kernel clears the flag when it re-parents a process
// (illumos-gate usr/src/uts/common/os/exit.c). An idle sweep costs one wait4
// answering ECHILD and allocates nothing (BENCH.md), so a second between
// sweeps bounds how long a zombie lasts at no cost worth weighing.
const timerSweepEvery time.Duration = time.Second
