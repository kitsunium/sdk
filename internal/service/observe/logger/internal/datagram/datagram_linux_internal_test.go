//go:build linux

package datagram

import (
	"net"
	"syscall"
	"testing"
)

// TestLinuxSendsABacklogInOneCallPerMaxBatch (#807): on Linux a socket with
// a descriptor is written with one sendmmsg(2) per MaxBatch datagrams — the
// syscall count the issue's strace measures — and a short count from the
// kernel is resumed from the first datagram it did not send.
func TestLinuxSendsABacklogInOneCallPerMaxBatch(t *testing.T) {
	t.Parallel()
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer server.Close()
	conn, err := net.Dial("udp", server.LocalAddr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	if NewBatchSender(conn).batch == nil {
		t.Fatal("a *net.UDPConn on Linux is not sent with sendmmsg")
	}
	batch := newMmsgBatch(conn)
	var calls []int
	batch.call = func(fd uintptr, headers []mmsghdr) (int, syscall.Errno) {
		calls = append(calls, len(headers))
		//: the kernel sends fewer than asked on the second call.
		if len(calls) == 2 {
			return sendmmsg(fd, headers[:3])
		}
		return sendmmsg(fd, headers)
	}
	payloads := make([][]byte, 2*MaxBatch+1)
	for i := range payloads {
		payloads[i] = []byte{byte(i)}
	}
	if sent, err := batch.send(payloads); err != nil || sent != len(payloads) {
		t.Fatalf("Send = %d, %v; want %d, nil", sent, err, len(payloads))
	}
	want := []int{MaxBatch, MaxBatch, len(payloads) - MaxBatch - 3}
	if len(calls) != len(want) {
		t.Fatalf("sendmmsg calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("sendmmsg calls = %v, want %v", calls, want)
		}
	}
}
