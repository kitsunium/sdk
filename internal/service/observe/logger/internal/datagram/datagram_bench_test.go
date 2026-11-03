package datagram_test

import (
	"net"
	"testing"

	"github.com/kitsunium/sdk/internal/service/observe/logger/internal/datagram"
)

// benchBatch is the backlog a network writer's drainer hands its transport
// at once (nettransport's streamBatchMax, journald's batchMax).
const benchBatch int = 16

// benchConn dials a connected udp socket at a loopback peer nobody reads:
// past its receive buffer the kernel drops what arrives, so the figure is
// the send's own cost.
func benchConn(b *testing.B) net.Conn {
	b.Helper()
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("ListenPacket: %v", err)
	}
	b.Cleanup(func() { _ = server.Close() })
	conn, err := net.Dial("udp", server.LocalAddr().String())
	if err != nil {
		b.Fatalf("Dial: %v", err)
	}
	b.Cleanup(func() { _ = conn.Close() })
	return conn
}

// benchPayloads is benchBatch log records of a typical JSON line's size.
func benchPayloads() [][]byte {
	payloads := make([][]byte, benchBatch)
	for i := range payloads {
		payloads[i] = []byte(`{"time":"2026-11-03T12:00:00Z","level":"INFO","msg":"request served","status":200}` + "\n")
	}
	return payloads
}

// BenchmarkSend_UDP_Batch16 sends 16 records as 16 datagrams through Send —
// one sendmmsg(2) on Linux — and reports the cost per record (#807).
func BenchmarkSend_UDP_Batch16(b *testing.B) {
	sender := datagram.NewBatchSender(benchConn(b))
	payloads := benchPayloads()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := sender.Send(payloads); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*benchBatch), "ns/record")
}

// BenchmarkSend_UDP_Each16 is the record-at-a-time baseline: the same 16
// datagrams, one Write — one sendto(2) — each.
func BenchmarkSend_UDP_Each16(b *testing.B) {
	conn := benchConn(b)
	payloads := benchPayloads()
	b.ReportAllocs()
	for b.Loop() {
		for _, p := range payloads {
			if _, err := conn.Write(p); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*benchBatch), "ns/record")
}
