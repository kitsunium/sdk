//go:build freebsd

// Package reaper — FreeBSD procctl(2) ABI constants for SetChildSubreaper.
package reaper

import "syscall"

// sysProcctl is the procctl(2) syscall number on FreeBSD. The stdlib syscall
// package exports it (SYS_PROCCTL = 544, from sys/sys/syscall.h /
// zsysnum_freebsd_amd64.go), so it is aliased rather than re-numbered. It is
// typed uintptr to feed syscall.Syscall6 directly.
const sysProcctl uintptr = syscall.SYS_PROCCTL

// procReapAcquire is PROC_REAP_ACQUIRE on FreeBSD: cmd value 2, from
// freebsd-src sys/sys/procctl.h (#define PROC_REAP_ACQUIRE 2 "reaping enable").
// It enables descendant reaping for the target process. It is typed uintptr to
// feed syscall.Syscall6 directly.
const procReapAcquire uintptr = 2
