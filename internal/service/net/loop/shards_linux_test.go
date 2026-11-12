//go:build linux

// Package loop — one SO_REUSEPORT listener per loop (#856): each loop
// accepts on its own queue, on every backend.
package loop

import (
	"fmt"
	"io"
	stdnet "net"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// networkShards is serveOn's name for a TCP group with one SO_REUSEPORT
// listener per loop.
const networkShards string = "tcp+shards"

// reuseportFDs binds n SO_REUSEPORT listeners on one loopback port, each with
// a backlog that holds the suite's queues, and returns them and the address.
func reuseportFDs(t testing.TB, n int) ([]int, string) {
	t.Helper()
	fds := make([]int, 0, n)
	port := 0
	for range n {
		fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, soReusePort, 1); err != nil {
			t.Fatal(err)
		}
		_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}, Port: port}); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Listen(fd, 4096); err != nil {
			t.Fatal(err)
		}
		if port == 0 {
			sa, err := syscall.Getsockname(fd)
			if err != nil {
				t.Fatal(err)
			}
			port = sa.(*syscall.SockaddrInet4).Port
		}
		fds = append(fds, fd)
	}
	return fds, fmt.Sprintf("127.0.0.1:%d", port)
}

// serveShards serves on loops loops of rings rings, each loop owning one
// SO_REUSEPORT listener.
func serveShards(t *testing.T, b backendCase, h corenet.LoopHandler, loops, rings int) (*Group, string) {
	t.Helper()
	fds, addr := reuseportFDs(t, loops)
	ls := make([]Listener, loops)
	for i, fd := range fds {
		ls[i] = Listener{FD: fd, Loop: i + 1}
	}
	g, err := Start(Config{Group: t.Name(), Value: corenet.LoopValue{Backend: b.backend, Loops: loops, Rings: rings}, Handler: h, Listeners: ls, Log: io.Discard})
	if err != nil {
		t.Fatalf("Start: %v %v", err, errs.FieldsOf(err))
	}
	t.Cleanup(func() { _ = g.Close() })
	return g, addr
}

// TestConformance_shards pins #856 on every backend: every loop accepts from
// its own listener, the reuseport-loops row is on, and a listener owned past
// the group's last loop is refused.
func TestConformance_shards(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		g, addr := serveShards(t, b, &lineH{}, 4, 1)
		if row := rowOf(g.State(), corenet.CapabilityReuseportLoops); row.Status != statusOn {
			t.Fatalf("reuseport-loops = %+v, want on", row)
		}
		for range 200 {
			c, err := stdnet.DialTimeout("tcp", addr, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			echo(t, c, "x\n")
			_ = c.Close()
		}
		accepted := make([]uint64, 4)
		for _, r := range g.State().RingStates {
			accepted[r.Loop] += r.Accepted
		}
		for i, n := range accepted {
			if n == 0 {
				t.Fatalf("loop %d accepted nothing: %v", i, accepted)
			}
		}
	})
	fds, _ := reuseportFDs(t, 1)
	_, err := Start(Config{Group: "g", Value: corenet.LoopValue{Loops: 2}, Handler: &lineH{}, Listeners: []Listener{{FD: fds[0], Loop: 3}}, Log: io.Discard})
	if !errs.HasCode(err, corenet.CodeLoopOptionUnsupported) {
		t.Fatalf("a listener past the last loop = %v, want LOOP_OPTION_UNSUPPORTED", err)
	}
}

// rowOf finds capability c in a group's report.
func rowOf(st corenet.LoopStateValue, c corenet.Capability) corenet.CapabilityValue {
	for _, row := range st.Capabilities {
		if row.Name == c {
			return row
		}
	}
	return corenet.CapabilityValue{}
}
