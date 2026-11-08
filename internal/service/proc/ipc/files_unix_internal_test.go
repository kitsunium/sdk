//go:build unix

package ipc

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// passDeadline bounds every wait of the descriptor-passing tests, so a defect
// is a failure and not a hang.
const passDeadline time.Duration = 5 * time.Second

// filePair connects a dialer to a listener on a private socket under the
// test's own directory and returns both ends, closed when the test ends.
func filePair(t *testing.T) (client, server *coreipc.Conn) {
	t.Helper()
	cfg := Config{Path: filepath.Join(t.TempDir(), "run", "files.sock")}
	ln, err := NewListener(&cfg)
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan *coreipc.Conn, 1)
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()
	client, err = Dial(context.Background(), &cfg)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server = <-accepted
	if server == nil {
		t.Fatal("Accept failed")
	}
	t.Cleanup(func() { _ = server.Close() })
	return client, server
}

// TestPassAListener pins #936's first criterion over a private socket under
// the test's own directory: a bound TCP listener passed with SendFiles is
// accepted on by the receiver, and a connection completes end to end — a
// message goes in and comes back. The sender's own listener and descriptor
// are closed before the dial, so what accepts is only the descriptor that
// crossed the socket. Where the kernel does not name the peer (darwin,
// OpenBSD, illumos, Solaris) the send is asserted refused, with nothing
// passed, and the test stops there: ADR 0148 makes the directory the gate on
// those. Windows cannot do it at all — the net package gives a socket no
// *os.File there (files_windows_external_test.go).
func TestPassAListener(t *testing.T) {
	t.Parallel()
	client, server := filePair(t)
	sender, ok := client.Conn.(coreipc.FileSender)
	receiver, rok := server.Conn.(coreipc.FileReceiver)
	if !ok || !rok {
		t.Fatal("a Unix connection offers no FileSender / FileReceiver")
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no TCP on the loopback: %v", err)
	}
	defer tcp.Close()
	addr := tcp.Addr().String()
	listenerFile, err := tcp.(*net.TCPListener).File()
	if err != nil {
		t.Fatalf("the listener has no file: %v", err)
	}
	defer listenerFile.Close()
	sendErr := sender.SendFiles([]byte("L"), listenerFile)
	if !kernelNamesPeer {
		if !errs.HasCode(sendErr, coreipc.CodeFDPassRefused) {
			t.Fatalf("SendFiles to a peer the kernel does not name = %v, want FD_PASS_REFUSED", sendErr)
		}
		t.Skip("this kernel names no peer, so no descriptor is passed (ADR 0148)")
	}
	if sendErr != nil {
		t.Fatalf("SendFiles = %v", sendErr)
	}
	buf := make([]byte, 8)
	n, files, err := receiver.RecvFiles(buf, 1)
	if err != nil || n != 1 || len(files) != 1 {
		t.Fatalf("RecvFiles = %d, %d files, %v; want the one listener", n, len(files), err)
	}
	passed, err := net.FileListener(files[0])
	_ = files[0].Close()
	if err != nil {
		t.Fatalf("the passed descriptor is no listener: %v", err)
	}
	defer passed.Close()
	//: the sender lets go of its two: from here the socket listens only
	//: through the descriptors the receiver holds.
	_ = listenerFile.Close()
	_ = tcp.Close()
	if err := passed.(*net.TCPListener).SetDeadline(time.Now().Add(passDeadline)); err != nil {
		t.Fatalf("SetDeadline = %v", err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		c, aerr := passed.Accept()
		if aerr != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()
	dialer := net.Dialer{Timeout: passDeadline}
	dialed, err := dialer.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("dialing the passed listener at %s: %v", addr, err)
	}
	defer dialed.Close()
	peer := <-accepted
	if peer == nil {
		t.Fatal("the receiver did not accept the connection")
	}
	defer peer.Close()
	for _, c := range []net.Conn{dialed, peer} {
		if err := c.SetDeadline(time.Now().Add(passDeadline)); err != nil {
			t.Fatalf("SetDeadline = %v", err)
		}
	}
	if _, err := dialed.Write([]byte("ping")); err != nil {
		t.Fatalf("Write = %v", err)
	}
	got := make([]byte, 4)
	if m, err := peer.Read(got); err != nil || string(got[:m]) != "ping" {
		t.Fatalf("the receiver read %q, %v; want ping", got[:m], err)
	}
	if _, err := peer.Write([]byte("pong")); err != nil {
		t.Fatalf("reply Write = %v", err)
	}
	if m, err := dialed.Read(got); err != nil || string(got[:m]) != "pong" {
		t.Fatalf("the dialer read %q, %v; want pong", got[:m], err)
	}
}
