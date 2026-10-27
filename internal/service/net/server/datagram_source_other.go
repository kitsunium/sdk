//go:build !linux

package server

import stdnet "net"

// newDatagramSource returns the portable one-datagram-per-syscall reader.
//
// Every non-Linux target lands here. The contract is identical; only the
// syscall count differs, and State reports the difference rather than hiding it.
// There is no GRO to ask for, so gro changes nothing.
//
// IFACE-PLUGIN: the signature is shared with datagram_source_linux.go, which
// picks one of two readers at run time; here there is only one to pick.
func newDatagramSource(pc stdnet.PacketConn, _ bool) datagramSource {
	//: the portable floor, available everywhere.
	return &portableReader{pc: pc}
}

// newDatagramSink returns the sink a socket's replies leave through: at once,
// one WriteTo each, unless batched — BatchReplies —, then queued and sent at
// the batch's end, one WriteTo each still: darwin's answer, as every other
// platform's without sendmmsg (ADR 0192).
func newDatagramSink(pc stdnet.PacketConn, batched bool, counters *datagramCounters, _ func()) datagramSink {
	//: the default: Reply writes at once and returns the write's error.
	if !batched {
		//: one datagram a call.
		return portableSink{pc: pc}
	}
	//: queued, one datagram a call at the batch's end.
	return newQueuedSink(pc, counters)
}

// batchAvailable reports whether the platform can read several datagrams per
// syscall. It feeds the degradation reported by State.
func batchAvailable() bool {
	//: no recvmmsg equivalent outside Linux.
	return false
}
