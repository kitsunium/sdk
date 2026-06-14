//go:build dragonfly

// Package reaper — DragonFly BSD procctl(2) ABI constants for SetChildSubreaper.
package reaper

// sysProcctl is the procctl(2) syscall number on DragonFly BSD. Unlike FreeBSD,
// the stdlib syscall package does NOT export SYS_PROCCTL for dragonfly, so it is
// numbered here from the kernel ABI: SYS_procctl = 536, from DragonFlyBSD
// sys/sys/syscall.h (generated from sys/kern/syscalls.master). It is typed
// uintptr to feed syscall.Syscall6 directly.
const sysProcctl uintptr = 536

// procReapAcquire is PROC_REAP_ACQUIRE on DragonFly BSD: cmd value 0x0001, from
// DragonFlyBSD sys/sys/procctl.h (#define PROC_REAP_ACQUIRE 0x0001 "enable
// reaping"). Note this differs from FreeBSD, where PROC_REAP_ACQUIRE is 2 — the
// two BSDs assigned different cmd numbers, so the constant is platform-local. It
// enables descendant reaping for the target process and is typed uintptr to feed
// syscall.Syscall6 directly.
const procReapAcquire uintptr = 0x0001
