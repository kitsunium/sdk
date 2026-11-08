//go:build linux || freebsd || dragonfly || netbsd

package ipc

// kernelNamesPeer: the kernel answers who is at the other end of a Unix
// socket here — SO_PEERCRED on Linux, LOCAL_PEERCRED on FreeBSD and
// DragonFly, LOCAL_PEEREID on NetBSD —, so a peer it did not name is refused.
const kernelNamesPeer bool = true
