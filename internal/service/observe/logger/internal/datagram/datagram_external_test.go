package datagram_test

import (
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/observe/logger/internal/datagram"
)

// readDatagrams reads n datagrams from pc, each whole, in arrival order.
func readDatagrams(t *testing.T, pc net.PacketConn, n int) []string {
	t.Helper()
	buf := make([]byte, 64<<10)
	got := make([]string, 0, n)
	if err := pc.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	for range n {
		k, _, err := pc.ReadFrom(buf)
		if err != nil {
			t.Fatalf("ReadFrom after %d datagram(s): %v", len(got), err)
		}
		got = append(got, string(buf[:k]))
	}
	return got
}

// backlog is n distinct payloads, the last one empty-free, sized apart.
func backlog(n int) [][]byte {
	payloads := make([][]byte, n)
	for i := range payloads {
		payloads[i] = []byte(fmt.Sprintf("record-%03d %s\n", i, strings.Repeat("x", i%7)))
	}
	return payloads
}

// TestSendKeepsEveryPayloadOneDatagram (#807): a backlog longer than
// MaxBatch reaches a connected udp peer as one datagram per payload, in
// order, none merged, and Send reports every one sent.
func TestSendKeepsEveryPayloadOneDatagram(t *testing.T) {
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
	sender := datagram.NewBatchSender(conn)
	payloads := backlog(datagram.MaxBatch + 9)
	sent, err := sender.Send(payloads)
	if err != nil || sent != len(payloads) {
		t.Fatalf("Send = %d, %v; want %d, nil", sent, err, len(payloads))
	}
	got := readDatagrams(t, server, len(payloads))
	for i, p := range payloads {
		if got[i] != string(p) {
			t.Fatalf("datagram %d = %q, want %q", i, got[i], p)
		}
	}
}

// TestSendOverAUnixDatagramSocket: journald's transport, a connected
// unixgram socket, takes the same path.
func TestSendOverAUnixDatagramSocket(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows' AF_UNIX is stream-only: journald refuses it before this (socket_windows.go)")
	}
	path := filepath.Join(t.TempDir(), "s")
	server, err := net.ListenPacket("unixgram", path)
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer server.Close()
	conn, err := net.Dial("unixgram", path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	payloads := backlog(5)
	if sent, err := datagram.NewBatchSender(conn).Send(payloads); err != nil || sent != 5 {
		t.Fatalf("Send = %d, %v; want 5, nil", sent, err)
	}
	for i, got := range readDatagrams(t, server, 5) {
		if got != string(payloads[i]) {
			t.Fatalf("datagram %d = %q, want %q", i, got, payloads[i])
		}
	}
}

// TestSendWithoutADescriptorWritesEach: a conn that exposes no descriptor —
// a test double, a wrapper — is written once per payload; a refusal ends the
// batch with the payloads before it counted.
func TestSendWithoutADescriptorWritesEach(t *testing.T) {
	t.Parallel()
	conn := &recordingConn{refuseAt: 2}
	sent, err := datagram.NewBatchSender(conn).Send(backlog(4))
	if sent != 2 || err == nil {
		t.Fatalf("Send = %d, %v; want 2 and the refusal", sent, err)
	}
	if len(conn.writes) != 2 {
		t.Fatalf("writes = %d, want 2", len(conn.writes))
	}
}

// TestSendOnAClosedSocketFails: the error of a closed descriptor surfaces
// and nothing is counted sent.
func TestSendOnAClosedSocketFails(t *testing.T) {
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
	sender := datagram.NewBatchSender(conn)
	if cerr := conn.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	if sent, err := sender.Send(backlog(3)); sent != 0 || err == nil {
		t.Fatalf("Send on a closed socket = %d, %v; want 0 and an error", sent, err)
	}
}

// recordingConn is a net.Conn with no descriptor that records each Write
// and refuses the refuseAt-th.
type recordingConn struct {
	net.Conn
	writes   []string
	refuseAt int
}

// Write records p, or refuses it.
func (c *recordingConn) Write(p []byte) (int, error) {
	if len(c.writes) == c.refuseAt {
		return 0, net.ErrClosed
	}
	c.writes = append(c.writes, string(p))
	return len(p), nil
}
