//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — a handler's close the loop's drain lingers on reads to its
// peer's end whatever became of its recv before the drain (#1609 review).
package loop

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdnet "net"
	"strconv"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// lingerBig is the answer the handler writes before its close: far more than
// the client's small receive buffer and the server's send buffer hold, so the
// loop still holds output when the drain begins.
const lingerBig = 4 << 20

// TestLinger_aHandlersCloseBeforeTheDrainReadsOn pins the linger of a
// handler's clean close made before the loop's drain, its output still
// leaving when the drain begins. On io_uring, a recv that ends while the
// connection is closing outside a drain is not re-armed (ring.reads): every
// completion in single-shot mode, an ENOBUFS, a multishot the kernel ended.
// The drain's linger then shut the writing side with no recv armed: the
// peer's later input stayed unread, and the close at drainLinger's bound
// sent a reset that discarded the answer the kernel still held for the peer.
// The linger now arms a recv of its own (uRing.halfClose); epoll and kqueue
// turn their read events back on there.
func TestLinger_aHandlersCloseBeforeTheDrainReadsOn(t *testing.T) {
	type tc struct {
		name string
		b    backendCase
		// singleShot switches uring-multishot-recv off.
		singleShot bool
	}
	cases := []tc{
		{name: "iouring single-shot", b: backendCase{"iouring", corenet.LoopBackendIOURing}, singleShot: true},
		{name: "iouring multishot", b: backendCase{"iouring", corenet.LoopBackendIOURing}},
		{name: "epoll", b: backendCase{"epoll", corenet.LoopBackendEpoll}},
	}
	runCase := func(t *testing.T, c tc) {
		//: a backend this kernel lacks.
		if err := probeBackend(c.b.backend); err != nil {
			t.Skipf("%s not present: %v", c.b.name, err)
		}
		g, addr := serve(t, c.b, &lineH{}, func(cfg *Config) {
			cfg.Value.Loops, cfg.Value.Rings, cfg.Value.OutputCap = 1, 1, 2*lingerBig
			//: one completion a recv, re-armed after each.
			if c.singleShot {
				cfg.Disabled = []corenet.Capability{corenet.CapabilityURingMultishotRecv}
			}
		})
		conn := dialSmallWindow(t, addr)
		//: the answer, then the handler's clean close, outside any drain.
		if _, err := conn.Write([]byte("BIG " + strconv.Itoa(lingerBig) + "\nCLOSE\n")); err != nil {
			t.Fatal(err)
		}
		lp := g.loops[0]
		slot := int32(-1)
		waitFor(t, "the handler's close, its output held", func() bool {
			var closing bool
			onLoop(t, g, func() {
				r := lp.rings[0]
				//: each slot, for the one its handler closed.
				for i := range r.conns {
					//: closing by its handler, outside the drain.
					if cc := &r.conns[i]; cc.state == stClosing && cc.reason == rNone {
						slot, closing = int32(i), cc.sending()
					}
				}
			})
			return closing
		})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shut := make(chan error, 1)
		go func() { shut <- g.Shutdown(ctx) }()
		waitFor(t, "the loop's drain", func() bool {
			var draining bool
			onLoop(t, g, func() { draining = lp.draining })
			return draining
		})
		//: input the linger must read, sent once the drain began.
		if _, err := conn.Write([]byte("more\n")); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 0, lingerBig)
		buf := make([]byte, 4096)
		lingering := false
		//: read until the loop's output is gone and the slot lingers; the
		//: kernel still holds the rest of the answer.
		for !lingering && len(got) < lingerBig {
			n, err := conn.Read(buf)
			got = append(got, buf[:n]...)
			//: the answer cut short before the linger.
			if err != nil {
				t.Fatalf("read %d of %d bytes before the linger: %v", len(got), lingerBig, err)
			}
			onLoop(t, g, func() { lingering = lp.rings[0].conns[slot].linger == lingerHalf })
		}
		//: the linger ends at its bound, the peer never ending.
		select {
		//: the drain ended by itself.
		case err := <-shut:
			//: without its context's deadline.
			if err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
		//: the drain hung.
		case <-time.After(10 * time.Second):
			t.Fatal("Shutdown did not return within 10 s")
		}
		rest, err := io.ReadAll(conn)
		got = append(got, rest...)
		//: every byte of the answer, then a clean end: no reset.
		if err != nil || !bytes.Equal(got, pattern(lingerBig)) {
			t.Fatalf("read %d of %d bytes, then %v: the close reset the answer", len(got), lingerBig, err)
		}
		resetClose([]stdnet.Conn{conn})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestURing_lingerArmsOneRecv pins the recv uRing.halfClose arms: one when
// the slot has none, none when its recv is still armed, and a starved entry
// of the slot re-armed no more once the linger armed its own (#1471: one recv
// a connection, or its bytes split between two).
func TestURing_lingerArmsOneRecv(t *testing.T) {
	b := backendCase{"iouring", corenet.LoopBackendIOURing}
	//: no io_uring here.
	if err := probeBackend(b.backend); err != nil {
		t.Skipf("io_uring not present: %v", err)
	}
	type tc struct {
		name string
		// singleShot leaves the handler's closed slot with no recv armed.
		singleShot bool
		// starved queues the slot as a recv starved while it was open.
		starved bool
		// want is the recvs armed by halfClose then rearmStarved.
		want int
	}
	cases := []tc{
		{name: "no recv armed", singleShot: true, want: 1},
		{name: "starved, then lingering", singleShot: true, starved: true, want: 1},
		{name: "multishot still armed", want: 0},
	}
	runCase := func(t *testing.T, c tc) {
		g, addr := serve(t, b, &lineH{}, func(cfg *Config) {
			cfg.Value.Loops, cfg.Value.Rings, cfg.Value.OutputCap = 1, 1, 2*lingerBig
			//: one completion a recv, re-armed after each.
			if c.singleShot {
				cfg.Disabled = []corenet.Capability{corenet.CapabilityURingMultishotRecv}
			}
		})
		conn := dialSmallWindow(t, addr)
		//: the handler's close, its output held by the small window.
		if _, err := conn.Write([]byte("BIG " + strconv.Itoa(lingerBig) + "\nCLOSE\n")); err != nil {
			t.Fatal(err)
		}
		rr := uringOf(g).rings[0]
		lp := g.loops[0]
		slot := int32(-1)
		waitFor(t, "the handler's close", func() bool {
			onLoop(t, g, func() {
				//: each slot, for the one its handler closed.
				for i := range rr.r.conns {
					//: closing by its handler.
					if cc := &rr.r.conns[i]; cc.state == stClosing && cc.reason == rNone {
						slot = int32(i)
					}
				}
			})
			return slot >= 0
		})
		var armed int
		onLoop(t, g, func() {
			cc := &rr.r.conns[slot]
			before := sum(rr.armedOn)
			//: a recv starved while the connection was open, still queued.
			if c.starved {
				rr.starved = append(rr.starved, starvedRecv{slot: slot, gen: cc.gen})
			}
			// The loop's drain, its linger's first step, as ring.lingers.
			lp.draining = true
			cc.linger = lingerHalf
			rr.halfClose(rr.r, slot)
			rr.rearmStarved()
			lp.draining = false
			armed = sum(rr.armedOn) - before
		})
		//: one recv for the lingering slot, never two.
		if armed != c.want {
			t.Fatalf("%d recvs armed for the lingering slot, want %d", armed, c.want)
		}
		resetClose([]stdnet.Conn{conn})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// dialSmallWindow connects with a 4 KiB receive buffer, set before the
// handshake so the window the server sees stays small.
func dialSmallWindow(t *testing.T, addr string) stdnet.Conn {
	t.Helper()
	d := stdnet.Dialer{Timeout: 3 * time.Second, Control: func(_, _ string, rc syscall.RawConn) error {
		var serr error
		err := rc.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4096)
		})
		return errors.Join(err, serr)
	}}
	c, err := d.Dial("tcp", addr)
	//: no connection.
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// sum is the total of v.
func sum(v []int) int {
	n := 0
	//: each count.
	for _, x := range v {
		n += x
	}
	return n
}
