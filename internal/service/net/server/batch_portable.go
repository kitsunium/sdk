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
