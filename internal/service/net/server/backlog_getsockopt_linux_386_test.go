// Package server — getsockopt(2) by number on linux/386, whose syscall package
// names only socketcall: the direct call is 365 since Linux 4.3.
package server

// sysGetsockopt is getsockopt(2)'s number on linux/386.
const sysGetsockopt uintptr = 365
