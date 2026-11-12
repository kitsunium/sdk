// Package server — what the portable backend counts in State (#975).
package server

import (
	"bytes"
	"fmt"
	"io"
	stdnet "net"
	"sync/atomic"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// counterH answers each newline-delimited request of a with b bytes.
type counterH struct {
	answer   []byte
	refusals atomic.Int64
}

func (h *counterH) OnBatchEnd(corenet.LoopIO)                            {}
func (h *counterH) OnOpen(corenet.LoopIO, corenet.LoopConnRef)           {}
func (h *counterH) OnWritable(corenet.LoopIO, corenet.LoopConnRef)       {}
func (h *counterH) OnClose(corenet.LoopIO, corenet.LoopConnRef, error)   {}
func (h *counterH) OnTimeout(io corenet.LoopIO, ref corenet.LoopConnRef) { _ = io.Close(ref) }
func (h *counterH) OnData(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	used := 0
	for {
		i := bytes.IndexByte(buf[used:], '\n')
		if i < 0 {
			return used
		}
		used += i + 1
		if err := io.Write(ref, h.answer); err != nil {
			h.refusals.Add(1)
		}
	}
}

// startCounting runs a portable group with h and returns its address and state.
func startCounting(t *testing.T, h corenet.LoopHandler, opts ...GroupOption) (string, func() corenet.LoopRingStateValue) {
	t.Helper()
	srv := New()
	t.Cleanup(func() { swallowErr(srv.Close()) })
	all := append([]GroupOption{Listen("tcp", "127.0.0.1:0"), Loop(corenet.LoopValue{Backend: corenet.LoopBackendPortable, Loops: 1})}, opts...)
	srv.Group("pl", all...).HandleLoop(h)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	return srv.State().Listeners[0].Address, func() corenet.LoopRingStateValue {
		st := srv.State()
		if len(st.Loops) != 1 || len(st.Loops[0].RingStates) != 1 {
			t.Fatalf("state = %+v", st)
		}
		return st.Loops[0].RingStates[0]
	}
}

// TestPortable_bytesInOut pins the first criterion on the portable backend: N
// requests of a bytes with answers of b bytes count N·a in and N·b out, and the
// pending gauge returns to 0 once every peer has read.
func TestPortable_bytesInOut(t *testing.T) {
	const n, a, b = 100, 32, 1500
	answer := bytes.Repeat([]byte("r"), b)
	addr, state := startCounting(t, &counterH{answer: answer})
	c, err := stdnet.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { swallowErr(c.Close()) })
	if err := c.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	request := append(bytes.Repeat([]byte("q"), a-1), '\n')
	buf := make([]byte, b)
	for range n {
		if _, err := c.Write(request); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(c, buf); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return state().PendingOutput == 0 })
	s := state()
	if s.BytesIn != n*a || s.BytesOut != n*b {
		t.Fatalf("BytesIn %d (want %d), BytesOut %d (want %d)", s.BytesIn, n*a, s.BytesOut, n*b)
	}
	if s.PendingOutput != 0 {
		t.Fatalf("PendingOutput = %d", s.PendingOutput)
	}
}

// waitFor polls cond for a little while.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestPortable_refusalCounters pins the second criterion on the portable
// backend: a Write refused with LoopOutputFull increments OutputRefused, a
// connection closed with LoopSpillFull SpillClosed, a fired SetDeadline
// Timeouts, and an idle close IdleClosed.
func TestPortable_refusalCounters(t *testing.T) {
	t.Run("output refused", func(t *testing.T) {
		answer := bytes.Repeat([]byte("r"), 32)
		addr, state := startCounting(t, &counterH{answer: answer})
		c := mustDial(t, addr)
		// A peer that never reads fills the output cap; requests go on arriving.
		line := bytes.Repeat([]byte("x"), 4096)
		line[len(line)-1] = '\n'
		deadline := time.Now().Add(15 * time.Second)
		for state().OutputRefused == 0 && time.Now().Before(deadline) {
			if err := c.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Write(bytes.Repeat(line, 8)); err != nil {
				break
			}
		}
		if got := state().OutputRefused; got == 0 {
			t.Fatal("no Write was refused")
		}
	})
	t.Run("spill full", func(t *testing.T) {
		addr, state := startCounting(t, &counterH{answer: []byte("x")})
		c := mustDial(t, addr)
		// A request with no newline fills the spill past its cap.
		if _, err := c.Write(bytes.Repeat([]byte("x"), portableSpillCap+1)); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return state().SpillClosed == 1 })
	})
	t.Run("timeout", func(t *testing.T) {
		addr, state := startCounting(t, &deadlineH{d: 20 * time.Millisecond})
		c := mustDial(t, addr)
		_ = c
		waitFor(t, func() bool { return state().Timeouts >= 1 })
	})
	t.Run("idle close", func(t *testing.T) {
		addr, state := startCounting(t, &counterH{answer: []byte("x")}, IdleTimeout(30*time.Millisecond))
		c := mustDial(t, addr)
		_ = c
		waitFor(t, func() bool { return state().IdleClosed == 1 })
	})
}

// deadlineH arms a deadline on open.
type deadlineH struct {
	counterH
	d time.Duration
}

// OnOpen arms the deadline.
func (h *deadlineH) OnOpen(io corenet.LoopIO, ref corenet.LoopConnRef) {
	_ = io.SetDeadline(ref, h.d)
}

// mustDial connects, or fails the test.
func mustDial(t *testing.T, addr string) stdnet.Conn {
	t.Helper()
	c, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(fmt.Sprint(err))
	}
	t.Cleanup(func() { swallowErr(c.Close()) })
	return c
}
