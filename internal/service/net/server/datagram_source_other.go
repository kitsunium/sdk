//go:build !linux

// Package server — batch-reader selection on platforms without recvmmsg.
package server

import stdnet "net"

// newDatagramSource returns the portable one-datagram-per-syscall reader.
//
// Every non-Linux target lands here. The contract is identical; only the
// syscall count differs, and State reports the difference rather than hiding it.
func newDatagramSource(pc stdnet.PacketConn) datagramSource {
	//: the portable floor, available everywhere.
	return &portableReader{pc: pc}
}

// batchAvailable reports whether the platform can read several datagrams per
// syscall. It feeds the degradation reported by State.
func batchAvailable() bool {
	//: no recvmmsg equivalent outside Linux.
	return false
}
