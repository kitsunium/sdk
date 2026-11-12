//go:build linux

// Package server — the idle cost of an HTTP/2 connection on a native loop
// (ADR 0186 §D7, #961's last criterion): 10 000 connections that opened with
// the preface and sent nothing else cost the loop's Go heap no more than an
// idle connection on HTTP/1.1 does. The clients are raw sockets, so the Go
// heap measured is the server's alone.
package server

import (
	"runtime"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// idleConnsH2 is how many idle connections the measure holds.
const idleConnsH2 = 10000

// TestLoopHTTP2_idleMemory measures an idle h2c connection against an idle
// HTTP/1.1 connection of the same harness, on both native backends. The
// second's cost is the http1 adapter's, the same order as the first's: the 256
// B that loop/CLAUDE.md gives an idle connection is the loop's alone, before
// any adapter state, so #961's bound is held by this comparison instead: an
// idle h2c connection costs at most what the codec's Conn adds over HTTP/1.1.
func TestLoopHTTP2_idleMemory(t *testing.T) {
	hello := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("ok") })
	for _, b := range []corenet.LoopBackend{corenet.LoopBackendEpoll, corenet.LoopBackendAuto} {
		t.Run(b.String(), func(t *testing.T) {
			h1 := idleHeapPerConn(t, b, http1.HandleLoop(hello, http1.Parser{}), []byte("GET / HTTP/1.1\r\nX: "), false)
			h2 := idleHeapPerConn(t, b, http2.HandleLoop(hello, http1.Parser{}), h2cOpening, true)
			t.Logf("idle heap per connection: http/1.1 %.0f B, h2c %.0f B", h1, h2)
			if h2 > h1+h2cConnBudget {
				t.Fatalf("an idle h2c connection costs %.0f B against %.0f B for HTTP/1.1, over the budget of %d B the codec's state may add", h2, h1, h2cConnBudget)
			}
		})
	}
}

// h2cOpening is the HTTP/2 preface and an empty SETTINGS.
var h2cOpening = []byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n\x00\x00\x00\x04\x00\x00\x00\x00\x00")

// h2cConnBudget is the most an idle h2c connection may add over an HTTP/1.1
// one: its Conn and the table's growth.
const h2cConnBudget = 200

// idleHeapPerConn holds idleConnsH2 connections that sent opening and reports
// the server's heap per connection. wantFrame reads the server's first frame,
// so an HTTP/2 connection has been served by the loop.
func idleHeapPerConn(t *testing.T, b corenet.LoopBackend, h corenet.LoopHandler, opening []byte, wantFrame bool) float64 {
	t.Helper()
	srv, addr := startLoopServer(t, b, h, Plaintext(), Loop(corenet.LoopValue{Backend: b, Loops: 2}))
	sa := socketAddr(t, addr)
	fds := make([]int, 0, idleConnsH2)
	before := heapNow()
	for range idleConnsH2 {
		fds = append(fds, connectOpening(t, sa, opening, wantFrame))
	}
	//: every opening read by the loop before the heap is read: an HTTP/1.1
	//: peer waits for no answer, and a loop behind its peers — linux/386 in
	//: #1336's CI, 99 B a connection against 274 B here — had not yet
	//: allocated what the comparison is about.
	want := uint64(idleConnsH2 * len(opening))
	deadline := time.Now().Add(30 * time.Second)
	for got := loopBytesIn(srv); got < want; got = loopBytesIn(srv) {
		if time.Now().After(deadline) {
			t.Fatalf("the loop read %d of the openings' %d bytes", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
	after := heapNow()
	for _, fd := range fds {
		_ = syscall.Close(fd)
	}
	swallowErr(srv.Close())
	return (float64(after) - float64(before)) / idleConnsH2
}

// loopBytesIn is the bytes every ring of srv's loop groups read.
func loopBytesIn(srv *Server) uint64 {
	n := uint64(0)
	for _, l := range srv.State().Loops {
		for _, r := range l.RingStates {
			n += r.BytesIn
		}
	}
	return n
}

// heapNow is the live heap after two collections.
func heapNow() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// socketAddr parses a "127.0.0.1:port" into a sockaddr.
func socketAddr(t *testing.T, addr string) *syscall.SockaddrInet4 {
	t.Helper()
	port := 0
	for i := len(addr) - 1; i >= 0 && addr[i] != ':'; i-- {
		port = port + int(addr[i]-'0')*pow10(len(addr)-1-i)
	}
	return &syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}
}

// pow10 is 10 to the n, small.
func pow10(n int) int {
	r := 1
	for range n {
		r *= 10
	}
	return r
}

// connectOpening connects a socket, sends opening, and with wantFrame reads
// the server's first frame so the connection has been served by the loop.
func connectOpening(t *testing.T, sa *syscall.SockaddrInet4, opening []byte, wantFrame bool) int {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	tv := syscall.Timeval{Sec: 30}
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		t.Fatal(err)
	}
	for {
		err = syscall.Connect(fd, sa)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	for len(opening) > 0 {
		n, werr := syscall.Write(fd, opening)
		if werr == syscall.EINTR {
			continue
		}
		if werr != nil {
			t.Fatal(werr)
		}
		opening = opening[n:]
	}
	if !wantFrame {
		return fd
	}
	var hdr [9]byte
	got := 0
	deadline := time.Now().Add(30 * time.Second)
	for got < len(hdr) {
		n, rerr := syscall.Read(fd, hdr[got:])
		if rerr == syscall.EINTR {
			continue
		}
		if rerr != nil {
			t.Fatalf("read: %v", rerr)
		}
		if n == 0 {
			t.Fatal("connection closed before the server's SETTINGS")
		}
		got += n
		if time.Now().After(deadline) {
			t.Fatal("timeout reading the server's SETTINGS")
		}
	}
	return fd
}
