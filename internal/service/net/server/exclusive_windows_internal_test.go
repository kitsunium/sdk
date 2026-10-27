//go:build windows

// Package server — SO_EXCLUSIVEADDRUSE, the graceful close and the bounded
// rebind, on the kernel they are for.
package server

import (
	"context"
	"io"
	stdnet "net"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// echoOnce answers one line's bytes and returns, so the engine closes the
// connection itself.
type echoOnce struct{}

func (echoOnce) ServeConn(_ context.Context, c corenet.Conn) error {
	buf := make([]byte, 64)
	n, err := c.Read(buf)
	if err != nil {
		return err
	}
	_, err = c.Write(buf[:n])
	return err
}

// startExclusive serves echoOnce on addr and a datagram group beside it.
func startExclusive(t *testing.T, addr string) *Server {
	t.Helper()
	srv := New()
	srv.Group("tcp", Listen("tcp", addr)).Handle(echoOnce{})
	srv.PacketGroup("udp", Listen("udp", "127.0.0.1:0")).HandleFunc(func(context.Context, corenet.Packet) error { return nil })
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start on %s: %v", addr, err)
	}
	return srv
}

// TestExclusiveAddr_windows pins SO_EXCLUSIVEADDRUSE: both a TCP listener and
// a unicast UDP socket report it, no other socket can bind their address
// while they live, and a socket this process did not set it on reads false.
func TestExclusiveAddr_windows(t *testing.T) {
	t.Parallel()
	srv := startExclusive(t, "127.0.0.1:0")
	t.Cleanup(func() { _ = srv.Close() })
	for _, l := range srv.State().Listeners {
		if !l.Exclusive {
			t.Errorf("%s %s: not exclusive", l.Network, l.Address)
		}
	}
	tcp := srv.State().Listeners[0].Address
	if ln, err := stdnet.Listen("tcp", tcp); err == nil {
		_ = ln.Close()
		t.Fatalf("a second socket bound %s", tcp)
	}
	plain, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plain.Close() }()
	if exclusiveOf(listenerSocket(plain)) {
		t.Fatal("a socket bound without the option reads exclusive")
	}
}

// TestRebind_windows pins the restart: a server that served a connection,
// closed it gracefully and stopped, is followed on the same address by
// another, its bind bounded and its wait reported.
func TestRebind_windows(t *testing.T) {
	t.Parallel()
	first := startExclusive(t, "127.0.0.1:0")
	addr := first.State().Listeners[0].Address
	for range 3 {
		c, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write([]byte("ping")); err != nil {
			t.Fatalf("write: %v", err)
		}
		got, err := io.ReadAll(c)
		_ = c.Close()
		if err != nil || string(got) != "ping" {
			t.Fatalf("echo %q, %v: the graceful close lost the answer", got, err)
		}
	}
	if err := first.Shutdown(t.Context()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	start := time.Now()
	second := startExclusive(t, addr)
	t.Cleanup(func() { _ = second.Close() })
	l := second.State().Listeners[0]
	t.Logf("rebound %s after %v, bind_wait %v", addr, time.Since(start), l.BindWait)
	if !l.Exclusive || l.Address != addr {
		t.Fatalf("rebound %+v", l)
	}
}
