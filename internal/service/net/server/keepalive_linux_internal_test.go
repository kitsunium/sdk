//go:build linux

package server

import (
	"context"
	stdnet "net"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// sockInt reads one integer socket option.
func sockInt(t *testing.T, c syscall.Conn, level, name int) int {
	t.Helper()
	raw, err := c.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var value int
	var gerr error
	if cerr := raw.Control(func(fd uintptr) { value, gerr = syscall.GetsockoptInt(int(fd), level, name) }); cerr != nil || gerr != nil {
		t.Fatalf("getsockopt %d/%d: %v %v", level, name, cerr, gerr)
	}
	return value
}

// acceptedOptions runs a goroutine group under opts and reports the accepted
// socket's options.
func acceptedOptions(t *testing.T, env map[string]string, opts ...GroupOption) (keepAlive, idle, userTimeout int) {
	t.Helper()
	got := make(chan [3]int, 1)
	srv := newServer(envOf(env))
	srv.Group("ka", append([]GroupOption{Listen("tcp", "127.0.0.1:0")}, opts...)...).HandleFunc(func(_ context.Context, c corenet.Conn) error {
		tc := c.(corenet.ConnUnwrapper).Unwrap().(*stdnet.TCPConn)
		got <- [3]int{
			sockInt(t, tc, syscall.SOL_SOCKET, syscall.SO_KEEPALIVE),
			sockInt(t, tc, syscall.IPPROTO_TCP, syscall.TCP_KEEPIDLE),
			sockInt(t, tc, syscall.IPPROTO_TCP, tcpUserTimeoutOption),
		}
		return nil
	})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	client, err := stdnet.DialTimeout("tcp", srv.State().Listeners[0].Address, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { swallowErr(client.Close()) })
	select {
	case v := <-got:
		return v[0], v[1], v[2]
	case <-time.After(10 * time.Second):
		t.Fatal("the handler never ran")
		return 0, 0, 0
	}
}

// TestKeepAlive_goroutineEngine pins the options on accepted goroutine-engine
// sockets: the standard library's default, a value, none, the user timeout's
// write-bound default and the switches.
func TestKeepAlive_goroutineEngine(t *testing.T) {
	for _, tc := range []struct {
		name             string
		env              map[string]string
		opts             []GroupOption
		wantKA, wantIdle int
		wantTimeoutMs    int
	}{
		{name: "default", wantKA: 1, wantIdle: 15, wantTimeoutMs: 0},
		{name: "explicit", opts: []GroupOption{KeepAlive(30 * time.Second)}, wantKA: 1, wantIdle: 30},
		{name: "negative", opts: []GroupOption{KeepAlive(-1)}, wantKA: 0, wantIdle: -1},
		{name: "user timeout", opts: []GroupOption{UserTimeout(1500 * time.Millisecond)}, wantKA: 1, wantIdle: -1, wantTimeoutMs: 1500},
		{name: "user timeout from write", opts: []GroupOption{WriteTimeout(2 * time.Second)}, wantKA: 1, wantIdle: -1, wantTimeoutMs: 2000},
		{
			name: "keepalive switched off", env: map[string]string{corenet.EnvDisable: string(corenet.CapabilityTCPKeepAlive)},
			opts: []GroupOption{KeepAlive(30 * time.Second)}, wantKA: 0, wantIdle: -1,
		},
		{
			name: "user timeout switched off", env: map[string]string{corenet.EnvDisable: string(corenet.CapabilityTCPUserTimeout)},
			opts: []GroupOption{UserTimeout(time.Second)}, wantKA: 1, wantIdle: -1, wantTimeoutMs: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ka, idle, timeout := acceptedOptions(t, tc.env, tc.opts...)
			if ka != tc.wantKA {
				t.Errorf("SO_KEEPALIVE = %d, want %d", ka, tc.wantKA)
			}
			if tc.wantIdle >= 0 && idle != tc.wantIdle {
				t.Errorf("TCP_KEEPIDLE = %d, want %d", idle, tc.wantIdle)
			}
			if timeout != tc.wantTimeoutMs {
				t.Errorf("TCP_USER_TIMEOUT = %d, want %d", timeout, tc.wantTimeoutMs)
			}
		})
	}
}

// TestKeepAlive_portableBackend pins the portable lane: a keepalive configured
// on the group reaches the accepted socket, and -1 turns it off, through the
// lane's open.
func TestKeepAlive_portableBackend(t *testing.T) {
	for _, tc := range []struct {
		name      string
		keepAlive time.Duration
		wantKA    int
		wantIdle  int
	}{
		{"explicit", 30 * time.Second, 1, 30},
		{"negative", -1, 0, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { swallowErr(ln.Close()) })
			client, err := stdnet.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { swallowErr(client.Close()) })
			c, err := ln.Accept()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { swallowErr(c.Close()) })
			tcp := c.(*stdnet.TCPConn)
			applyAcceptedKeepAlive(tcp, tc.keepAlive)
			if got := sockInt(t, tcp, syscall.SOL_SOCKET, syscall.SO_KEEPALIVE); got != tc.wantKA {
				t.Errorf("SO_KEEPALIVE = %d, want %d", got, tc.wantKA)
			}
			if tc.wantIdle >= 0 {
				if got := sockInt(t, tcp, syscall.IPPROTO_TCP, syscall.TCP_KEEPIDLE); got != tc.wantIdle {
					t.Errorf("TCP_KEEPIDLE = %d, want %d", got, tc.wantIdle)
				}
			}
		})
	}
	t.Run("wired from the group", func(t *testing.T) {
		srv := New()
		t.Cleanup(func() { swallowErr(srv.Close()) })
		srv.Group("pl", Listen("tcp", "127.0.0.1:0"), KeepAlive(30*time.Second),
			Loop(corenet.LoopValue{Backend: corenet.LoopBackendPortable})).HandleLoop(&loopLineH{})
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		if got := srv.loopEngines[0].(portableEngine).g.keepAlive; got != 30*time.Second {
			t.Fatalf("keepAlive = %v", got)
		}
		st := srv.State()
		if len(st.Loops) != 1 {
			t.Fatalf("state = %+v", st)
		}
	})
}

// TestKeepAlive_loopOptionRefusal pins the kinds a datagram group refuses.
func TestKeepAlive_datagramGroupRefuses(t *testing.T) {
	srv := New()
	srv.PacketGroup("p", Listen("udp", "127.0.0.1:0"), KeepAlive(time.Second)).
		HandleFunc(func(context.Context, corenet.Packet) error { return nil })
	err := srv.Start(t.Context())
	t.Cleanup(func() { swallowErr(srv.Close()) })
	if err == nil {
		t.Fatal("a datagram group accepted KeepAlive")
	}
}
