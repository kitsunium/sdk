//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package server — a peer that vanishes with output unsent (#971): on the
// veth pair of net-kernel-gate.yml's bench job (scripts/ci/net-veth.sh, root),
// the client's socket lives in the pair's far namespace and the far leg is set
// down once the server has queued 1 MiB for it. TCP_USER_TIMEOUT, inherited
// from the listener, bounds how long the kernel retransmits into the void: the
// connection fails within 3 s of a 1 s user timeout on every loop backend and
// on the goroutine engine, and stays open with the row switched off. Nothing
// here runs without root and the pair: the tests skip, naming what they need.
package server

import (
	"context"
	stdnet "net"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// The pair net-veth.sh sets up: its namespace, the far leg in it, and the two
// addresses, read from the environment the script prints.
const (
	blackholeNS      = "netgate-l3"
	blackholeFarLink = "ng-b"
	// blackholeOutput is what the server queues before the leg goes down.
	blackholeOutput = 1 << 20
	// blackholeBound is how soon the connection must fail after the leg went
	// down with a user timeout of a second.
	blackholeBound = 3 * time.Second
	// blackholeControl is how long the control keeps the connection open with
	// the row switched off.
	blackholeControl = 60 * time.Second
)

// blackholePair is the veth pair's addresses, or a skip naming what is
// missing.
func blackholePair(t *testing.T) (local, peer string) {
	t.Helper()
	local, peer = os.Getenv("KITSUNIUM_NETGATE_L3_LOCAL"), os.Getenv("KITSUNIUM_NETGATE_L3_PEER")
	switch {
	case os.Geteuid() != 0:
		t.Skip("needs root: the far leg of the veth pair is set down and up")
	case local == "" || peer == "":
		t.Skip("needs the veth pair of scripts/ci/net-veth.sh (KITSUNIUM_NETGATE_L3_LOCAL, KITSUNIUM_NETGATE_L3_PEER)")
	}
	if _, err := os.Stat("/run/netns/" + blackholeNS); err != nil {
		t.Skipf("needs the namespace %s of scripts/ci/net-veth.sh: %v", blackholeNS, err)
	}
	return local, peer
}

// farLeg sets the veth pair's far leg up or down; the far leg is the pair's
// own, created by the CI step for this job and nothing else.
func farLeg(t *testing.T, state string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "ip", "-n", blackholeNS, "link", "set", blackholeFarLink, state).CombinedOutput(); err != nil {
		t.Fatalf("ip link set %s %s: %v %s", blackholeFarLink, state, err, out)
	}
}

// dialFromFar connects to addr from a socket created in the pair's far
// namespace, its receive buffer small so the server's output stays queued: a
// goroutine locked to a thread enters the namespace, dials, and returns
// without unlocking, so the runtime ends that thread rather than reuse it.
func dialFromFar(t *testing.T, addr string) stdnet.Conn {
	t.Helper()
	type result struct {
		c   stdnet.Conn
		err error
	}
	got := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		ns, err := os.Open("/run/netns/" + blackholeNS)
		if err != nil {
			got <- result{err: err}
			return
		}
		defer func() { _ = ns.Close() }()
		if _, _, errno := syscall.RawSyscall(sysSetns(), ns.Fd(), syscall.CLONE_NEWNET, 0); errno != 0 {
			got <- result{err: errno}
			return
		}
		d := stdnet.Dialer{Timeout: 5 * time.Second, Control: func(_, _ string, rc syscall.RawConn) error {
			var serr error
			if err := rc.Control(func(fd uintptr) { serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4096) }); err != nil {
				return err
			}
			return serr
		}}
		c, err := d.Dial("tcp", addr)
		got <- result{c: c, err: err}
	}()
	r := <-got
	if r.err != nil {
		t.Fatalf("dial %s from %s: %v", addr, blackholeNS, r.err)
	}
	t.Cleanup(func() { _ = r.c.Close() })
	return r.c
}

// sysSetns is setns(2)'s number, absent from the standard library: 308 on
// amd64, 268 in the generic table the other three architectures share.
func sysSetns() uintptr {
	if runtime.GOARCH == "amd64" {
		return 308
	}
	return 268
}

// floodH queues blackholeOutput bytes at OnOpen and records the close.
type floodH struct {
	laneEchoH
	closed chan error
}

func (h *floodH) OnOpen(lio corenet.LoopIO, ref corenet.LoopConnRef) {
	h.laneEchoH.OnOpen(lio, ref)
	_ = lio.Write(ref, make([]byte, blackholeOutput))
}

func (h *floodH) OnClose(lio corenet.LoopIO, ref corenet.LoopConnRef, err error) {
	h.laneEchoH.OnClose(lio, ref, err)
	select {
	case h.closed <- err:
	default:
	}
}

// blackholeCase is one engine the blackhole runs on.
type blackholeCase struct {
	name string
	env  map[string]string
	// declare declares the group listening on addr, and returns the
	// connection's failure as the engine reports it.
	declare func(srv *Server, addr string, timeout time.Duration) <-chan error
}

// blackholeCases are the loop backends and the goroutine engine.
func blackholeCases() []blackholeCase {
	loopCase := func(name string, b corenet.LoopBackend, env map[string]string) blackholeCase {
		return blackholeCase{name: name, env: env, declare: func(srv *Server, addr string, timeout time.Duration) <-chan error {
			h := &floodH{closed: make(chan error, 1)}
			srv.Group("loop", Listen("tcp", addr), UserTimeout(timeout),
				Loop(corenet.LoopValue{Backend: b, Loops: 1, OutputCap: 2 * blackholeOutput})).HandleLoop(h)
			return h.closed
		}}
	}
	return []blackholeCase{
		loopCase("iouring-direct", corenet.LoopBackendIOURing, nil),
		loopCase("iouring-fds", corenet.LoopBackendIOURing, map[string]string{corenet.EnvDisable: string(corenet.CapabilityURingDirect)}),
		loopCase("epoll", corenet.LoopBackendEpoll, nil),
		loopCase("portable", corenet.LoopBackendPortable, nil),
		{name: "goroutine", declare: func(srv *Server, addr string, timeout time.Duration) <-chan error {
			failed := make(chan error, 1)
			srv.Group("conn", Listen("tcp", addr), UserTimeout(timeout)).HandleFunc(func(_ context.Context, c corenet.Conn) error {
				chunk := make([]byte, 64<<10)
				for {
					if _, err := c.Write(chunk); err != nil {
						failed <- err
						return err
					}
				}
			})
			return failed
		}},
	}
}

// blackholeOnce serves one connection from the far namespace on c, waits for
// the server's output to queue, sets the far leg down and returns how long the
// engine took to report the failure, or false when it did not within limit.
func blackholeOnce(t *testing.T, c blackholeCase, local string, timeout, limit time.Duration) (time.Duration, error, bool) {
	t.Helper()
	srv := newServer(envOf(c.env))
	failed := c.declare(srv, stdnet.JoinHostPort(local, "0"), timeout)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	//: the stage says whether io_uring's connections are direct here.
	for _, l := range srv.State().Loops {
		t.Logf("%s: backend %v, probe stage %q", c.name, l.Backend, l.ProbeStage)
	}
	conn := dialFromFar(t, srv.State().Listeners[0].Address)
	// The output fills the peer's small window, then the server's send
	// buffer: what is left is unsent when the leg goes down.
	time.Sleep(300 * time.Millisecond)
	farLeg(t, "down")
	t.Cleanup(func() { farLeg(t, "up") })
	down := time.Now()
	select {
	case err := <-failed:
		return time.Since(down), err, true
	case <-time.After(limit):
		swallowErr(conn.SetDeadline(time.Now()))
		return limit, nil, false
	}
}

// TestBlackhole_userTimeout holds #971's second and third criteria: with a
// user timeout of 1 s, a connection whose peer vanished with 1 MiB unsent
// fails within 3 s — OnClose with a socket error on io_uring direct, io_uring
// on process descriptors, epoll and portable, the handler's Write on the
// goroutine engine.
func TestBlackhole_userTimeout(t *testing.T) {
	local, _ := blackholePair(t)
	for _, c := range blackholeCases() {
		t.Run(c.name, func(t *testing.T) {
			took, err, ok := blackholeOnce(t, c, local, time.Second, blackholeBound)
			if !ok {
				t.Fatalf("no failure within %v of the peer vanishing", blackholeBound)
			}
			if err == nil {
				t.Fatalf("the connection ended cleanly %v after the peer vanished, want a socket error", took)
			}
			t.Logf("failed %v after the peer vanished: %v", took.Round(time.Millisecond), err)
		})
	}
}

// TestBlackhole_controlWithoutTheRow is the control: tcp-user-timeout switched
// off, the same connection is still open after 60 s, the kernel's own
// retransmissions running for minutes.
func TestBlackhole_controlWithoutTheRow(t *testing.T) {
	local, _ := blackholePair(t)
	c := blackholeCases()[2]
	c.env = map[string]string{corenet.EnvDisable: string(corenet.CapabilityTCPUserTimeout)}
	if took, err, ok := blackholeOnce(t, c, local, time.Second, blackholeControl); ok {
		t.Fatalf("the connection failed %v after the peer vanished with the row switched off: %v", took, err)
	}
	t.Logf("still open %v after the peer vanished, the row switched off", blackholeControl)
}
