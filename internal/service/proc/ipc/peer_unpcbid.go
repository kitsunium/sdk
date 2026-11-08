//go:build netbsd

package ipc

import (
	"encoding/binary"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
)

// LOCAL_PEEREID (sys/un.h, 0x0003) answers a struct unpcbid
// { pid_t unp_pid; uid_t unp_euid; gid_t unp_egid; }.
const (
	peerOption    uintptr = 0x0003
	unpcbidSize   int     = 12
	unpPIDOffset  int     = 0
	unpEUIDOffset int     = 4
	unpEGIDOffset int     = 8
)

// peerFromBuffer decodes a struct unpcbid.
func peerFromBuffer(b []byte, order binary.ByteOrder) (coreipc.PeerValue, bool) {
	if len(b) < unpcbidSize {
		return coreipc.PeerValue{}, false
	}
	return coreipc.PeerValue{
		PID: int(int32(order.Uint32(b[unpPIDOffset:]))), UID: int(order.Uint32(b[unpEUIDOffset:])),
		GID: int(order.Uint32(b[unpEGIDOffset:])), Verified: true,
	}, true
}
