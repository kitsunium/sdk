//go:build linux

package server

import (
	stdnet "net"
	"testing"
)

// benchBatch is how many datagrams each iteration reads. It is the whole point
// of the comparison: the portable reader needs one syscall per datagram, the
// batched one needs a single recvmmsg for all of them.
const benchBatch int = 16

// benchDatagramSize is a payload comfortably under any MTU.
const benchDatagramSize int = 512

// datagramPair sets up a bound socket and a client already dialled to it.
func datagramPair(b *testing.B) (server *stdnet.UDPConn, client stdnet.Conn) {
	b.Helper()
	pc, err := stdnet.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("listen: %v", err)
	}
	udp, ok := pc.(*stdnet.UDPConn)
	if !ok {
		b.Fatalf("expected a *net.UDPConn, got %T", pc)
	}
	c, derr := stdnet.Dial("udp", udp.LocalAddr().String())
	if derr != nil {
		b.Fatalf("dial: %v", derr)
	}
	b.Cleanup(func() {
		swallowErr(c.Close())
		swallowErr(udp.Close())
	})
	return udp, c
}

// fill queues benchBatch datagrams on the socket so the following read has
// something already waiting — the regime where batching can pay off at all.
func fill(b *testing.B, client stdnet.Conn) {
	b.Helper()
	payload := make([]byte, benchDatagramSize)
	for range benchBatch {
		if _, err := client.Write(payload); err != nil {
			b.Fatalf("write: %v", err)
		}
	}
}

// drain reads exactly benchBatch datagrams through the given source.
func drain(b *testing.B, source datagramSource, slots []datagram) {
	b.Helper()
	read := 0
	for read < benchBatch {
		n, err := source.readBatch(slots)
		if err != nil {
			b.Fatalf("readBatch: %v", err)
		}
		read += n
	}
}

// BenchmarkDatagramRead_Batched measures reading benchBatch queued datagrams
// through recvmmsg — in principle a single syscall.
func BenchmarkDatagramRead_Batched(b *testing.B) {
	udp, client := datagramPair(b)
	source := newDatagramSource(udp)
	if _, ok := source.(*multiReader); !ok {
		b.Fatalf("expected the batched reader, got %T", source)
	}
	slots := newSlots(benchBatch, benchDatagramSize)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		b.StopTimer()
		fill(b, client)
		b.StartTimer()
		drain(b, source, slots)
	}
}

// BenchmarkDatagramRead_Portable is the baseline: the same datagrams read one
// ReadFrom at a time, which is what every non-Linux target does.
func BenchmarkDatagramRead_Portable(b *testing.B) {
	udp, client := datagramPair(b)
	source := &portableReader{pc: udp}
	slots := newSlots(benchBatch, benchDatagramSize)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		b.StopTimer()
		fill(b, client)
		b.StartTimer()
		drain(b, source, slots)
	}
}
