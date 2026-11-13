//go:build linux

// Package server — the idle cost of an HTTP/2 connection on a native loop
// (ADR 0186 §D7, #961's last criterion): 10 000 connections that opened with
// the preface and sent nothing else cost the loop's Go heap no more than an
// idle connection on HTTP/1.1 does. The clients are raw sockets, so the Go
// heap measured is the server's alone.
package server

import (
	"errors"
	"io"
	stdnet "net"
	"runtime"
	"slices"
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
// HTTP/1.1 connection of the same harness, on epoll. The second's cost is the
// http1 adapter's, the same order as the first's: the 256 B that
// loop/CLAUDE.md gives an idle connection is the loop's alone, before any
// adapter state, so #961's bound is held by this comparison instead: an idle
// h2c connection costs at most what the codec's Conn adds over HTTP/1.1.
//
// Each figure is the median of idleRounds readings taken in alternation, each
// after the process settled (settle): a reading is the live heap's growth
// over a window, and memory an earlier test of the binary releases inside
// that window lowers it — #1354's HTTP/1.1 reading of 97 B on linux/386, a
// third of what the same harness reads alone (TestIdleHeapPerConn_release).
//
// The other native backend is TestLoopHTTP2_idleMemoryAuto's: one test held
// both, 120 000 connections and twelve readings, the most expensive test of
// server_test, and the shard Bazel gave it reached the 60 s of .bazelrc under
// coverage (#1372); two tests are two shards.
func TestLoopHTTP2_idleMemory(t *testing.T) {
	idleMemoryOn(t, corenet.LoopBackendEpoll)
}

// TestLoopHTTP2_idleMemoryAuto is TestLoopHTTP2_idleMemory on the backend
// Auto chooses: io_uring where the kernel has it.
func TestLoopHTTP2_idleMemoryAuto(t *testing.T) {
	idleMemoryOn(t, corenet.LoopBackendAuto)
}

// idleMemoryOn is TestLoopHTTP2_idleMemory's comparison on backend b.
func idleMemoryOn(t *testing.T, b corenet.LoopBackend) {
	t.Helper()
	hello := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("ok") })
	var h1s, h2s []float64
	for range idleRounds {
		h1s = append(h1s, idleHeapPerConn(t, b, http1.HandleLoop(hello, http1.Parser{}), []byte("GET / HTTP/1.1\r\nX: "), false, nil))
		h2s = append(h2s, idleHeapPerConn(t, b, http2.HandleLoop(hello, http1.Parser{}), h2cOpening, true, nil))
	}
	h1, h2 := median(h1s), median(h2s)
	t.Logf("%s: idle heap per connection: http/1.1 %.0f B, h2c %.0f B (medians of %v and %v)", b, h1, h2, h1s, h2s)
	if h2 > h1+h2cConnBudget {
		t.Fatalf("%s: an idle h2c connection costs %.0f B against %.0f B for HTTP/1.1, over the budget of %d B the codec's state may add", b, h2, h1, h2cConnBudget)
	}
}

// TestIdleHeapPerConn_release reproduces #1354's cause and holds its remedy:
// memory an earlier test kept, released inside a reading's window, lowers
// that reading by its size over the connections — the HTTP/1.1 side falling,
// the comparison failing with nothing wrong in the codec —, and the median of
// three readings, one of them so disturbed, stays with the undisturbed ones.
//
// The quiet reading it compares against is itself such a median: a single
// one is exposed to the same releases (noble's 6.8 lane read 279 B quiet
// against 194 B disturbed, a fall of 85 B where 2 MiB over 10 000
// connections is 210 B). The release is releaseBytes, twice that, so the
// fall it causes stands well clear of what a stray release does to a
// reading, and each verdict is judged against the fall measured: half of
// what the release should cost for the cause, half of the fall read for the
// remedy.
func TestIdleHeapPerConn_release(t *testing.T) {
	hello := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("ok") })
	h1 := func(during func()) float64 {
		return idleHeapPerConn(t, corenet.LoopBackendEpoll, http1.HandleLoop(hello, http1.Parser{}), []byte("GET / HTTP/1.1\r\nX: "), false, during)
	}
	quiets := make([]float64, 0, idleRounds)
	for range idleRounds {
		quiets = append(quiets, h1(nil))
	}
	quiet := median(quiets)
	release := func() {
		held = nil
	}
	held = make([]byte, releaseBytes)
	disturbed := h1(release)
	t.Logf("HTTP/1.1 reading: %.0f B quiet (median of %v), %.0f B with %d B released in the window", quiet, quiets, disturbed, releaseBytes)
	cost := float64(releaseBytes) / idleConnsH2
	if fall := quiet - disturbed; fall < cost/2 {
		t.Fatalf("a release of %.0f B a connection in the window moved the reading from %.0f B to %.0f B: the cause is not this", cost, quiet, disturbed)
	}
	held = make([]byte, releaseBytes)
	rounds := []float64{h1(release), h1(nil), h1(nil)}
	if m, tol := median(rounds), (quiet-disturbed)/2; m < quiet-tol || m > quiet+tol {
		t.Fatalf("median of %v = %.0f B, want within %.0f B of the quiet %.0f B (half the fall a release caused)", rounds, m, tol, quiet)
	}
}

// releaseBytes is what TestIdleHeapPerConn_release releases inside a
// reading's window: 4 MiB, about 420 B over idleConnsH2 connections.
const releaseBytes = 4 << 20

// held is memory an earlier test kept, released by TestIdleHeapPerConn_release
// inside a reading's window.
var held []byte

// idleRounds is how many readings of each kind a median is taken over.
const idleRounds = 3

// median is the middle of xs, sorted.
func median(xs []float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[len(s)/2]
}

// settleHeap waits until the process stops ending goroutines — an earlier test's
// servers winding down — collecting as it goes, for at most two seconds.
func settleHeap() {
	deadline := time.Now().Add(2 * time.Second)
	steady := 0
	for n := runtime.NumGoroutine(); steady < 3 && time.Now().Before(deadline); {
		heapNow()
		time.Sleep(10 * time.Millisecond)
		m := runtime.NumGoroutine()
		if m == n {
			steady++
		} else {
			steady = 0
		}
		n = m
	}
}

// h2cOpening is the HTTP/2 preface and an empty SETTINGS.
var h2cOpening = []byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n\x00\x00\x00\x04\x00\x00\x00\x00\x00")

// h2cConnBudget is the most an idle h2c connection may add over an HTTP/1.1
// one: its Conn and the table's growth.
const h2cConnBudget = 200

// idleHeapPerConn holds idleConnsH2 connections that sent opening and reports
// the server's heap per connection. wantFrame reads the server's first frame,
// so an HTTP/2 connection has been served by the loop. during, when not nil,
// runs inside the window, once the baseline is read.
func idleHeapPerConn(t *testing.T, b corenet.LoopBackend, h corenet.LoopHandler, opening []byte, wantFrame bool, during func()) float64 {
	t.Helper()
	srv, addr := startLoopServer(t, b, h, Plaintext(), Loop(corenet.LoopValue{Backend: b, Loops: 2}))
	sa := socketAddr(t, addr)
	fds := make([]int, 0, idleConnsH2)
	settleHeap()
	before := heapNow()
	if during != nil {
		during()
	}
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
		resetClose(fd)
	}
	swallowErr(srv.Close())
	return (float64(after) - float64(before)) / idleConnsH2
}

// resetClose closes a client socket with a reset (SO_LINGER 0) rather than a
// FIN: neither end keeps a TIME_WAIT socket, and the connection-tracking
// entry a netfilter host keeps for the connection lives seconds (CLOSE)
// rather than two minutes (TIME_WAIT). The two readings of this file open
// 160 000 connections a run: closed by FIN, they held most of a runner's
// 262 144 conntrack entries for two minutes, and the loop suite go test runs
// beside this one, at its own heavy tests, filled the rest — every new
// loopback flow refused, a datagram's send failing with EPERM (#1377).
func resetClose(fd int) {
	//: a socket that refuses the option is closed all the same.
	_ = syscall.SetsockoptLinger(fd, syscall.SOL_SOCKET, syscall.SO_LINGER, &syscall.Linger{Onoff: 1, Linger: 0})
	_ = syscall.Close(fd)
}

// TestResetClose_leavesNoTimeWait holds #1377's remedy where it shows
// without privilege: the server's end of a client closed by FIN reads the
// end of the stream, one closed by resetClose reads a reset, and no
// TIME_WAIT socket is left on either end of the latter.
//
// The control is the end the server reads, not a TIME_WAIT socket: whether
// the kernel keeps one after a FIN is its own budget's call — past
// tcp_max_tw_buckets, or killing orphans under pressure, it keeps none, and
// noble 6.8's lane, after its suites' tens of thousands of connections, kept
// none for 32 FIN closes. The FIN closes' TIME_WAIT sockets are logged.
func TestResetClose_leavesNoTimeWait(t *testing.T) {
	const n = 32
	closes := func(closeClient func(fd int)) (ends, resets, timeWaits int) {
		ln, err := stdnet.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ln.Close() }()
		addr := ln.Addr().String()
		sa := socketAddr(t, addr)
		fds := make([]int, 0, n)
		//: nothing sent: a socket closed over unread bytes resets, FIN or not.
		for range n {
			fds = append(fds, connectOpening(t, sa, nil, false))
		}
		accepted := make([]stdnet.Conn, 0, n)
		for range n {
			c, err := ln.Accept()
			if err != nil {
				t.Fatal(err)
			}
			accepted = append(accepted, c)
		}
		for _, fd := range fds {
			closeClient(fd)
		}
		var b [1]byte
		for _, c := range accepted {
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, err := c.Read(b[:])
			switch {
			case errors.Is(err, io.EOF):
				ends++
			case errors.Is(err, syscall.ECONNRESET):
				resets++
			}
			_ = c.Close()
		}
		at := procHexAddrPort(t, addr)
		for _, s := range procSockets(t, "tcp") {
			if s.state == "06" && (s.local == at || s.remote == at) {
				timeWaits++
			}
		}
		return ends, resets, timeWaits
	}
	//: the control: closed by FIN, every server end reads the end of the stream.
	ends, resets, tw := closes(func(fd int) { _ = syscall.Close(fd) })
	t.Logf("%d clients closed by FIN: %d ends, %d resets read, %d TIME_WAIT sockets left", n, ends, resets, tw)
	if ends != n {
		t.Fatalf("%d of %d clients closed by FIN were read as an end (%d resets): the witness tells nothing apart", ends, n, resets)
	}
	//: the remedy: every server end reads a reset, and nothing waits.
	ends, resets, tw = closes(resetClose)
	if resets != n || tw != 0 {
		t.Fatalf("%d clients closed by resetClose: %d resets read, %d ends, %d TIME_WAIT sockets left; want %d resets and none left", n, resets, ends, tw, n)
	}
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
