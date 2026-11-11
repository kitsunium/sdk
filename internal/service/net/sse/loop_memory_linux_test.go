//go:build linux && !race

package sse_test

import (
	"context"
	stdnet "net"
	"runtime"
	"runtime/metrics"
	"strings"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// streamHeapBudget is the most Go heap one open stream may cost.
	streamHeapBudget = 512
	// openStreams is how many streams the test holds open.
	openStreams = 10000
)

// heapObjects reads the live heap after two collections.
func heapObjects() uint64 {
	runtime.GC()
	runtime.GC()
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

// raiseNoFile lifts the soft descriptor limit as far as the hard one allows,
// for this process alone, reporting the limit reached.
func raiseNoFile(t *testing.T, need uint64) uint64 {
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

// dialRaw opens a connection with plain syscalls, so no client object of the
// runtime's lands on the heap the test measures, and sends req.
func dialRaw(addr [4]byte, port int, req []byte) (int, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if err := syscall.Connect(fd, &syscall.SockaddrInet4{Port: port, Addr: addr}); err != nil {
		_ = syscall.Close(fd)
		return -1, err
	}
	if _, err := syscall.Write(fd, req); err != nil {
		_ = syscall.Close(fd)
		return -1, err
	}
	return fd, nil
}

// TestLoopSSE_tenThousandStreamsAndTheirDrain is the acceptance: every stream
// open costs under 512 B of Go heap, and the drain ends each one within its
// grace, every client having read a final retry: field.
func TestLoopSSE_tenThousandStreamsAndTheirDrain(t *testing.T) {
	if lim := raiseNoFile(t, 2*openStreams+1024); lim < 2*openStreams+256 {
		t.Skipf("RLIMIT_NOFILE %d is below what %d streams need", lim, openStreams)
	}
	for _, e := range engines[:2] { // the budget is the native backends'
		t.Run(e.name, func(t *testing.T) {
			src := &countingSource{}
			srv, addr := startSSE(t, e.backend, src, corenet.LoopValue{})
			host, portStr, err := stdnet.SplitHostPort(addr)
			if err != nil {
				t.Fatal(err)
			}
			ip := stdnet.ParseIP(host).To4()
			port := 0
			for _, c := range portStr {
				port = port*10 + int(c-'0')
			}
			var addr4 [4]byte
			copy(addr4[:], ip)
			before := heapObjects()
			fds := make([]int, 0, openStreams)
			defer func() {
				for _, fd := range fds {
					_ = syscall.Close(fd)
				}
			}()
			req := []byte("GET /e HTTP/1.1\r\nHost: h\r\n\r\n")
			for range openStreams {
				fd, err := dialRaw(addr4, port, req)
				if err != nil {
					t.Fatalf("after %d connections: %v", len(fds), err)
				}
				fds = append(fds, fd)
			}
			waitTrue(t, func() bool { return src.opened.Load() == openStreams })
			after := heapObjects()
			per := float64(int64(after)-int64(before)) / openStreams
			t.Logf("%s: %d open streams, %.1f B of Go heap each", e.name, openStreams, per)
			if per > streamHeapBudget {
				t.Fatalf("an open stream costs %.1f B, budget %d", per, streamHeapBudget)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
			waitTrue(t, func() bool { return src.closed.Load() == openStreams })
			buf := make([]byte, 1<<16)
			for i, fd := range fds {
				var tv syscall.Timeval
				tv.Sec = 5
				_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
				var seen strings.Builder
				for {
					n, err := syscall.Read(fd, buf)
					if n <= 0 || err != nil {
						break
					}
					seen.Write(buf[:n])
				}
				if !strings.Contains(seen.String(), "retry: 3000\n\n") {
					t.Fatalf("client %d read %q", i, seen.String())
				}
			}
		})
	}
}
