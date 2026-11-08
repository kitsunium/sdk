//go:build !linux && !freebsd && !dragonfly && !netbsd

package ipc

// kernelNamesPeer: no peer credential is read here — darwin (raw syscalls
// deprecated), OpenBSD (pinned syscalls), illumos and Solaris
// (getpeerucred is libc-only) —, so the directory is the gate (ADR 0148).
const kernelNamesPeer bool = false
