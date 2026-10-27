//go:build linux

// Package loop — the drain (plan P6): the listen queue taken before the
// listener closes, and the connections it held served, on every backend.
package loop

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdnet "net"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// drainQueued is how many connections wait in the listen queue as the drain
// begins: more than epoll takes in one readiness (epollAcceptBurst) and than
// io_uring keeps accepts in flight (acceptDepth), so the drain's own take is
// what reaches the last of them.
const drainQueued int = 3 * epollAcceptBurst

// TestConformance_drainTakesQueue pins the drain's order on every backend:
// with the loop held, drainQueued connections complete their handshake and
// send one line into the listen queue, the drain is asked for, then the loop
// goes on. Every queued connection must read its echo, then the end — never a
// reset, never an end before the echo —, the listener must refuse once the
// drain is over, and Shutdown must return within its budget. Before plan P6
// the listener left the set unread and closed last, and a connection the
// drain found unread was closed at once: both reset the peer.
func TestConformance_drainTakesQueue(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		gate := make(chan struct{})
		held := make(chan struct{}, 1)
		var holding atomic.Bool
		h := &lineH{}
		h.batchEnd = func(corenet.LoopIO) {
			// The first batch end once asked holds the loop on the gate.
			if holding.CompareAndSwap(true, false) {
				held <- struct{}{}
				<-gate
			}
		}
		g, addr := serve(t, b, h, nil)
		c0 := dial(t, addr)
		echo(t, c0, "warm\n")
		holding.Store(true)
		_, _ = c0.Write([]byte("hold\n"))
		select {
		case <-held:
		case <-time.After(5 * time.Second):
			close(gate)
			t.Fatal("the loop never reached its batch end")
		}

		conns := make([]stdnet.Conn, 0, drainQueued)
		for range drainQueued {
			c, err := stdnet.DialTimeout("tcp", addr, 3*time.Second)
			if err != nil {
				close(gate)
				t.Fatalf("dial into the queue: %v", err)
			}
			_ = c.SetDeadline(time.Now().Add(20 * time.Second))
			t.Cleanup(func() { _ = c.Close() })
			_, _ = c.Write([]byte("q\n"))
			conns = append(conns, c)
		}
		done := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done <- g.Shutdown(ctx)
		}()
		// The drain request lands while the loop is held; then it goes on.
		time.Sleep(20 * time.Millisecond)
		close(gate)

		var wg sync.WaitGroup
		failures := make(chan string, drainQueued+1)
		for i, c := range conns {
			wg.Go(func() {
				if msg := echoThenEnd(c, "q\n"); msg != "" {
					failures <- msg + " (queued #" + strconv.Itoa(i) + ")"
				}
			})
		}
		wg.Go(func() {
			if msg := echoThenEnd(c0, "hold\n"); msg != "" {
				failures <- msg + " (the held one)"
			}
		})
		wg.Wait()
		close(failures)
		var all []string
		for f := range failures {
			all = append(all, f)
		}
		if len(all) > 0 {
			t.Fatalf("%d of %d connections cut short: %v\n%s", len(all), drainQueued+1, all[:min(len(all), 8)], dumpGroup(g))
		}
		if err := <-done; err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		if c, err := stdnet.DialTimeout("tcp", addr, time.Second); err == nil {
			_ = c.Close()
			t.Fatal("the listener still accepts after the drain")
		}
		o, cl := h.counts()
		if o != drainQueued+1 || cl != o {
			t.Fatalf("%d opened, %d closed, want %d each", o, cl, drainQueued+1)
		}
	})
}

// echo writes line on c and requires it back.
func echo(t *testing.T, c stdnet.Conn, line string) {
	t.Helper()
	_, _ = c.Write([]byte(line))
	got := make([]byte, len(line))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != line {
		t.Fatalf("echo = %q, %v", got, err)
	}
}

// echoThenEnd reads want then the end of the stream from c, and says what
// went wrong: empty when the echo came whole and the end was a clean one.
func echoThenEnd(c stdnet.Conn, want string) string {
	got := make([]byte, len(want))
	if _, err := io.ReadFull(c, got); err != nil {
		return "echo: " + err.Error()
	}
	if !bytes.Equal(got, []byte(want)) {
		return "echo " + string(got)
	}
	rest, err := io.ReadAll(c)
	switch {
	case errors.Is(err, syscall.ECONNRESET):
		return "reset after the echo"
	case err != nil:
		return "end: " + err.Error()
	case len(rest) > 0:
		return "bytes past the echo: " + string(rest)
	}
	return ""
}
