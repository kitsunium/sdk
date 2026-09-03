//go:build linux

// Package server — batch-reader selection on Linux.
package server

import stdnet "net"

// newDatagramSource returns the batched reader when the socket exposes a raw
// descriptor, and the portable one otherwise.
//
// The type assertion is the honest guard: recvmmsg needs a real file
// descriptor, and a caller can legitimately hand us a PacketConn that has none
// — a test double, or a wrapper. Falling back keeps such a socket working
// instead of failing at the first read.
func newDatagramSource(pc stdnet.PacketConn) datagramSource {
	raw, ok := pc.(rawConnProvider)
	//: no raw descriptor means no recvmmsg; the portable path still serves.
	if !ok {
		//: the portable floor still serves this socket correctly.
		return &portableReader{pc: pc}
	}
	reader, err := newMultiReader(pc, raw)
	//: if the descriptor cannot be reached, degrade rather than fail.
	if err != nil {
		//: degrade rather than fail a socket we can still read one at a time.
		return &portableReader{pc: pc}
	}
	//: one syscall per batch, which is the whole point on this platform.
	return reader
}

// batchAvailable reports whether the platform can read several datagrams per
// syscall. It feeds the degradation reported by State.
func batchAvailable() bool {
	//: Linux has recvmmsg.
	return true
}
