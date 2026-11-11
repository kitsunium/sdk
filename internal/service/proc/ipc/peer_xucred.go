//go:build freebsd || dragonfly

package ipc

import (
	"encoding/binary"
	"unsafe"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
)

// LOCAL_PEERCRED (sys/un.h, 0x0001) answers a struct xucred
// (sys/ucred.h): u_int cr_version; uid_t cr_uid; short cr_ngroups;
// gid_t cr_groups[XU_NGROUPS]; then, on FreeBSD 13 and later, a union whose
// pid_t cr_pid sits at the pointer's alignment.
const (
	peerOption       uintptr = 0x0001
	xucredVersion    uint32  = 0
	xuNGroups        int     = 16
	xuVersionOffset  int     = 0
	xuUIDOffset      int     = 4
	xuNGroupsOffset  int     = 8
	xuGroupsOffset   int     = 12
	xuGroupsEnd      int     = xuGroupsOffset + 4*xuNGroups
	pointerAlignment int     = int(unsafe.Sizeof(uintptr(0)))
	xuPIDOffset      int     = (xuGroupsEnd + pointerAlignment - 1) / pointerAlignment * pointerAlignment
)

// peerFromBuffer decodes a struct xucred: the uid, the first group (the
// effective gid), and the pid where the kernel wrote one (FreeBSD >= 13; a
// DragonFly struct ends before it and leaves PID 0).
func peerFromBuffer(b []byte, order binary.ByteOrder) (coreipc.PeerValue, bool) {
	if len(b) < xuGroupsEnd || order.Uint32(b[xuVersionOffset:]) != xucredVersion {
		return coreipc.PeerValue{}, false
	}
	ngroups := int(int16(order.Uint16(b[xuNGroupsOffset:])))
	gid := -1
	if ngroups > 0 {
		gid = int(order.Uint32(b[xuGroupsOffset:]))
	}
	peer := coreipc.PeerValue{UID: int(order.Uint32(b[xuUIDOffset:])), GID: gid, Verified: true}
	if len(b) >= xuPIDOffset+4 {
		peer.PID = int(int32(order.Uint32(b[xuPIDOffset:])))
	}
	return peer, true
}

// groupsFromBuffer decodes the groups of a struct xucred: cr_groups, the
// effective gid first, at most XU_NGROUPS (16) — a peer in more groups is
// judged on the first sixteen, which admits fewer, never more.
func groupsFromBuffer(b []byte, order binary.ByteOrder) []int {
	if len(b) < xuGroupsEnd || order.Uint32(b[xuVersionOffset:]) != xucredVersion {
		return nil
	}
	n := min(max(int(int16(order.Uint16(b[xuNGroupsOffset:]))), 0), xuNGroups)
	groups := make([]int, n)
	for i := range n {
		groups[i] = int(order.Uint32(b[xuGroupsOffset+4*i:]))
	}
	return groups
}
