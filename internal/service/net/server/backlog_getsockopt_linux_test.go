//go:build linux && !386

// Package server — getsockopt(2)'s number where the syscall package names it.
package server

import "syscall"

// sysGetsockopt is getsockopt(2)'s number.
const sysGetsockopt uintptr = syscall.SYS_GETSOCKOPT
