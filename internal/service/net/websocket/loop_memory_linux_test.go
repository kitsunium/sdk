//go:build linux && !race

package websocket_test

import (
	"context"
	"fmt"
	stdnet "net"
	"runtime"
	"runtime/metrics"
	"syscall"
	"testing"
	"time"
)

const (
	// connHeapBudget is the most Go heap an idle connection may cost.
	connHeapBudget = 512
	// idleConns is how many connections the test holds.
	idleConns = 10000
)

func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

func raiseFiles(t *testing.T, need uint64) uint64 {
	t.Helper()
	var r syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &r); err != nil {
		t.Fatal(err)
	}
	r.Cur = min(r.Max, need)
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &r); err != nil {
		t.Fatal(err)
	}
	return r.Cur
}

// dialRaw opens a connection with plain syscalls and writes msg: nothing of it
// is on the Go heap this test measures.
func dialRaw(addr [4]byte, port int, msg []byte) (int, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if err := syscall.Connect(fd, &syscall.SockaddrInet4{Port: port, Addr: addr}); err != nil {
		_ = syscall.Close(fd)
		return -1, err
	}
	if _, err := syscall.Write(fd, msg); err != nil {
		_ = syscall.Close(fd)
		return -1, err
	}
	return fd, nil
}

// TestLoopWS_tenThousandIdleConnections is the acceptance: 10 000 connections on
// the loop under 512 B of Go heap each, a ping from each one answered, and no
// goroutine per connection.
func TestLoopWS_tenThousandIdleConnections(t *testing.T) {
	if lim := raiseFiles(t, 2*idleConns+1024); lim < 2*idleConns+256 {
		t.Skipf("RLIMIT_NOFILE %d is below what %d connections need", lim, idleConns)
	}
	for _, b := range loopBackends[:2] {
		t.Run(b.name, func(t *testing.T) {
			src := &loopEcho{}
			srv, addr := startWS(t, b.backend, src)
			host, portStr, err := stdnet.SplitHostPort(addr)
			if err != nil {
				t.Fatal(err)
			}
			ip := stdnet.ParseIP(host).To4()
			var addr4 [4]byte
			copy(addr4[:], ip)
			port := 0
			fmt.Sscanf(portStr, "%d", &port)
			goBefore := runtime.NumGoroutine()
			before := liveHeap()
			fds := make([]int, 0, idleConns)
			defer func() {
				for _, fd := range fds {
					_ = syscall.Close(fd)
				}
			}()
			for range idleConns {
				fd, err := dialRaw(addr4, port, []byte(wsHandshake))
				if err != nil {
					t.Fatalf("after %d connections: %v", len(fds), err)
				}
				fds = append(fds, fd)
			}
			deadline := time.Now().Add(30 * time.Second)
			for src.opened.Load() != idleConns {
				if time.Now().After(deadline) {
					t.Fatalf("%d opened", src.opened.Load())
				}
				time.Sleep(10 * time.Millisecond)
			}
			after := liveHeap()
			per := float64(int64(after)-int64(before)) / idleConns
			gr := runtime.NumGoroutine() - goBefore
			t.Logf("%s: %d connections, %.1f B of Go heap each, %d goroutines more", b.name, idleConns, per, gr)
			if per > connHeapBudget {
				t.Fatalf("an idle connection costs %.1f B, budget %d", per, connHeapBudget)
			}
			if gr > 16 {
				t.Fatalf("%d goroutines for %d connections", gr, idleConns)
			}
			//: a ping from each, a pong to each.
			mask := []byte{1, 2, 3, 4}
			ping := append([]byte{0x89, 0x80}, mask...)
			buf := make([]byte, 4096)
			for i, fd := range fds {
				if _, err := syscall.Write(fd, ping); err != nil {
					t.Fatalf("ping %d: %v", i, err)
				}
			}
			for i, fd := range fds {
				var tv syscall.Timeval
				tv.Sec = 5
				_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
				got := 0
				for {
					n, err := syscall.Read(fd, buf[got:])
					if n <= 0 || err != nil {
						t.Fatalf("conn %d: read %d: %v", i, n, err)
					}
					got += n
					if idx := indexPong(buf[:got]); idx >= 0 {
						break
					}
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
		})
	}
}

// indexPong returns the offset of a pong frame in b, past the HTTP head, or -1.
func indexPong(b []byte) int {
	for i := range len(b) - 1 {
		if b[i] == 0x8A && b[i+1] == 0 {
			return i
		}
	}
	return -1
}
