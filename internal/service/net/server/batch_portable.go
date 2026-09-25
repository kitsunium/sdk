// Package server — the portable datagram reader.
package server

import (
	stdnet "net"
)

// portableReader reads one datagram per syscall using the standard library.
//
// It is the floor every platform has, and the fallback whenever the batched
// path is unavailable. Correctness is identical; only the syscall count differs,
// which is exactly what the benchmarks measure.
type portableReader struct {
	// pc is the socket being read.
	pc stdnet.PacketConn
}

// readBatch implements datagramSource by reading a single datagram.
//
// It deliberately does not loop to fill the batch: a second ReadFrom would block
// until another datagram arrived, turning a low-traffic socket into a stalled
// one. Batching is only a win when the kernel already has the datagrams queued,
// which is precisely what recvmmsg reports and ReadFrom cannot.
func (r *portableReader) readBatch(slots []datagram) (n int, err error) {
	read, addr, rerr := r.pc.ReadFrom(slots[0].buf)
	//: Windows FAILS a read whose datagram outgrew the buffer where every
	//: Unix truncates it in silence. It is the same event — a datagram past
	//: the ceiling — so it is reported the same way: as a slot filled to the
	//: probe byte, which dispatch drops and counts. Handed up as an error it
	//: was skipped by the loop and never counted at all.
	if rerr != nil && datagramTruncated(rerr) {
		slots[0].n = len(slots[0].buf)
		slots[0].addr = addr
		//: one oversized datagram, for dispatch to drop and count.
		return 1, nil
	}
	//: a read failure ends the batch; the loop above decides whether to stop.
	if rerr != nil {
		//: hand the failure up so the loop can classify it.
		return 0, rerr
	}
	slots[0].n = read
	slots[0].addr = addr
	//: exactly one datagram per call on the portable path.
	return 1, nil
}
