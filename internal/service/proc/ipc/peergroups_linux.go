//go:build linux

package ipc

import (
	"net"
	"syscall"
	"unsafe"
)

// soPeerGroups is SO_PEERGROUPS (include/uapi/asm-generic/socket.h, Linux
// 4.13): 59 on every Linux port Go supports — parisc, sparc and alpha, the
// ports that number it otherwise, are not Go's.
const soPeerGroups uintptr = 59

// Buffers of SO_PEERGROUPS: the first holds a usual account's groups; a
// longer list is read once more at the length the kernel asked for, bounded
// by NGROUPS_MAX; a group id is four bytes.
const (
	firstPeerGroups int = 64
	maxPeerGroups   int = 65536
	gidBytes        int = 4
)

// peerGroups asks the kernel for the supplementary groups of the process at
// the other end of c (#1092): SO_PEERGROUPS, the group list of the very
// credentials SO_PEERCRED reads — recorded when the peer connected or
// listened, never read again from a process whose pid may since name another
// one. nil when the kernel does not answer (before 4.13): the peer is then
// judged on its effective group alone, which admits fewer, never more. A list
// longer than the first buffer is read once more at the length the kernel
// asked for.
func peerGroups(c *net.UnixConn) []int {
	raw, err := c.SyscallConn()
	if err != nil {
		return nil
	}
	groups, need := readPeerGroups(raw, firstPeerGroups)
	if groups == nil && need > firstPeerGroups && need <= maxPeerGroups {
		groups, _ = readPeerGroups(raw, need)
	}
	return groups
}

// readPeerGroups is one SO_PEERGROUPS read into room groups: the groups, or
// nil and — when the kernel said the list is longer (ERANGE) — how many it
// holds.
func readPeerGroups(raw syscall.RawConn, room int) (groups []int, need int) {
	buf := make([]uint32, room)
	size := uint32(room * gidBytes)
	var errno syscall.Errno
	if err := raw.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall6(getsockoptTrap, fd, syscall.SOL_SOCKET, soPeerGroups,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0)
	}); err != nil {
		return nil, 0
	}
	need = int(size) / gidBytes
	switch {
	case errno == syscall.ERANGE:
		return nil, need
	case errno != 0 || need > room:
		return nil, 0
	}
	groups = make([]int, need)
	for i := range need {
		groups[i] = int(buf[i])
	}
	return groups, need
}
