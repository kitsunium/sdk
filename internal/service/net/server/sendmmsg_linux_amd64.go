//go:build linux && amd64

package server

// sysSendmmsg is sendmmsg(2)'s number on linux/amd64
// (arch/x86/entry/syscalls/syscall_64.tbl), which the standard library's
// syscall package does not define on this architecture.
const sysSendmmsg uintptr = 307
