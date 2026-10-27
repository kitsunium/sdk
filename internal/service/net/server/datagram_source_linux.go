//go:build linux

package server

import stdnet "net"

// newDatagramSource returns the batched reader when the socket exposes a raw
// descriptor, and the portable one otherwise. With gro, the batched reader
// switches UDP_GRO on, which only it can read: it alone parses the segment size
// out of each message and splits it (ADR 0192).
//
// The type assertion is the honest guard: recvmmsg needs a real file
// descriptor, and a caller can legitimately hand us a PacketConn that has none
// — a test double, or a wrapper. Falling back keeps such a socket working
// instead of failing at the first read.
func newDatagramSource(pc stdnet.PacketConn, gro bool) datagramSource {
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
	//: a socket that is not UDP, or a kernel before 5.0, keeps one datagram a
	//: message.
	if gro {
		reader.enableGRO()
	}
	//: one syscall per batch, which is the whole point on this platform.
	return reader
}

// newDatagramSink returns the sink a socket's replies leave through: at once,
// one WriteTo each, unless batched — BatchReplies —, then per batch: the
// sendmmsg and GSO sink when the socket is UDP and exposes a raw descriptor, the
// queued one otherwise. onGSOOff is called once the device refuses GSO for the
// socket.
func newDatagramSink(pc stdnet.PacketConn, batched bool, counters *datagramCounters, onGSOOff func()) datagramSink {
	//: the default: Reply writes at once and returns the write's error.
	if !batched {
		//: one WriteTo a reply.
		return portableSink{pc: pc}
	}
	raw, ok := pc.(rawConnProvider)
	//: no raw descriptor means no sendmmsg.
	if !ok {
		//: queued, one WriteTo each at the batch's end.
		return newQueuedSink(pc, counters)
	}
	writer, err := newMultiWriter(pc, raw, counters, onGSOOff)
	//: a Unix datagram socket, or one whose descriptor cannot be reached.
	if err != nil {
		//: queued, one WriteTo each at the batch's end.
		return newQueuedSink(pc, counters)
	}
	//: one sendmmsg a batch.
	return writer
}

// batchAvailable reports whether the platform can read several datagrams per
// syscall. It feeds the degradation reported by State.
func batchAvailable() bool {
	//: Linux has recvmmsg.
	return true
}
