//go:build linux && 386

package server

// sysSendmmsg is sendmmsg(2)'s number on linux/386
// (arch/x86/entry/syscalls/syscall_32.tbl), which the standard library's
// syscall package does not define on this architecture.
const sysSendmmsg uintptr = 345
