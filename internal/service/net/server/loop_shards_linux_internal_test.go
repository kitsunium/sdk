//go:build linux

// Package server — Shards(0) on a loop group, one SO_REUSEPORT listener per
// loop (#856).
package server

import (
	stdnet "net"
	"strings"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// reusePortOf reads SO_REUSEPORT back from a listener.
func reusePortOf(t *testing.T, ln stdnet.Listener) int {
	t.Helper()
	sc, ok := ln.(syscall.Conn)
	if !ok {
		t.Fatalf("%T has no descriptor", ln)
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var v int
	var gerr error
	if err := rc.Control(func(fd uintptr) { v, gerr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, 15) }); err != nil {
		t.Fatal(err)
	}
	if gerr != nil {
		t.Fatal(gerr)
	}
	return v
}

// TestLoopShards_perLoopListeners pins Shards(0): 4 listeners on one port,
// SO_REUSEPORT read back on each, the row on, one row of Shards 4 in State,
// and every loop accepting from its own queue.
func TestLoopShards_perLoopListeners(t *testing.T) {
	srv := newServer(envOf(nil))
	g := srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Shards(0), Loop(corenet.LoopValue{Loops: 4})).HandleLoop(&loopLineH{})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	_ = g
	st := srv.State()
	if len(st.Listeners) != 1 || st.Listeners[0].Shards != 4 {
		t.Fatalf("listeners = %+v, want one row, Shards 4", st.Listeners)
	}
	if n := len(srv.listeners); n != 4 {
		t.Fatalf("bound %d listeners, want 4", n)
	}
	port := srv.listeners[0].ln.Addr().String()
	for _, b := range srv.listeners {
		if reusePortOf(t, b.ln) != 1 {
			t.Fatalf("listener %s without SO_REUSEPORT", b.ln.Addr())
		}
		if b.ln.Addr().String() != port {
			t.Fatalf("listener on %s, want %s", b.ln.Addr(), port)
		}
	}
	ls := srv.State().Loops[0]
	if !hasCapability(ls.Capabilities, corenet.CapabilityReuseportLoops, statusOn) {
		t.Fatalf("capabilities = %+v, want reuseport-loops on", ls.Capabilities)
	}
	conns := make([]stdnet.Conn, 0, 200)
	for range 200 {
		c, err := stdnet.DialTimeout("tcp", port, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := c.Write([]byte("hi\n")); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 3)
		if _, err := c.Read(buf); err != nil || string(buf) != "hi\n" {
			t.Fatalf("echo %q %v", buf, err)
		}
	}
	accepted := make([]uint64, 4)
	for _, r := range srv.State().Loops[0].RingStates {
		accepted[r.Loop] += r.Accepted
	}
	for i, n := range accepted {
		if n == 0 {
			t.Fatalf("loop %d accepted nothing: %v", i, accepted)
		}
	}
	_ = conns
}

// TestLoopShards_disabledByOperator pins the token: one shared listener, the
// row disabled.
func TestLoopShards_disabledByOperator(t *testing.T) {
	srv := newServer(envOf(map[string]string{corenet.EnvDisable: string(corenet.CapabilityReuseportLoops)}))
	srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Shards(0), Loop(corenet.LoopValue{Loops: 2})).HandleLoop(&loopLineH{})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	st := srv.State()
	if len(st.Listeners) != 1 || st.Listeners[0].Shards != 1 || len(srv.listeners) != 1 {
		t.Fatalf("listeners = %+v (%d bound), want one", st.Listeners, len(srv.listeners))
	}
	rows := st.Loops[0].Capabilities
	if !hasCapability(rows, corenet.CapabilityReuseportLoops, statusDisabled) {
		t.Fatalf("capabilities = %+v, want reuseport-loops disabled", rows)
	}
	if !strings.Contains(srv.listeners[0].ln.Addr().String(), "127.0.0.1") {
		t.Fatalf("listener = %s", srv.listeners[0].ln.Addr())
	}
}
