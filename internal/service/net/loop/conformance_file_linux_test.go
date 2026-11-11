//go:build linux

package loop

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	stdnet "net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// fileH answers a line "FILE n" with a head then n bytes of its file, kept
// open by the test; OnWritable and OnClose are counted.
type fileH struct {
	lineH
	fd       int
	writable atomic.Int64
	refused  atomic.Value
	closeErr atomic.Value
	head     []byte
}

func (h *fileH) OnData(lio corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	if i := bytes.IndexByte(buf, '\n'); i >= 0 && bytes.HasPrefix(buf, []byte("FILE ")) {
		n, _ := strconv.ParseInt(string(buf[5:i]), 10, 64)
		fw := lio.(corenet.LoopFileWriter)
		if err := fw.WriteFile(ref, h.head, corenet.LoopFileSpanValue{FD: h.fd, Len: n}); err != nil {
			panic(err)
		}
		if err := fw.WriteFile(ref, nil, corenet.LoopFileSpanValue{FD: h.fd, Len: 1}); !errors.Is(err, corenet.LoopOutputFull) {
			h.refused.Store(fmt.Sprintf("second WriteFile: %v", err))
		}
		if err := lio.Write(ref, []byte("x")); !errors.Is(err, corenet.LoopOutputFull) {
			h.refused.Store(fmt.Sprintf("Write during a file: %v", err))
		}
		return i + 1
	}
	return len(buf)
}

func (h *fileH) OnWritable(lio corenet.LoopIO, ref corenet.LoopConnRef) {
	h.writable.Add(1)
	_ = lio.Close(ref)
}

func (h *fileH) OnClose(_ corenet.LoopIO, _ corenet.LoopConnRef, err error) {
	h.closeErr.Store(fmt.Sprint(err))
}

// tempFile writes n pattern bytes to a file in dir and opens it read-only.
func tempFile(t *testing.T, n int) *os.File {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, pattern(n), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func dialT(t *testing.T, addr string) stdnet.Conn {
	t.Helper()
	c, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	return c
}

// fdCount is the number of open descriptors of the test process.
func fdCount(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc/self/fd: %v", err)
	}
	return len(ents)
}

// TestWriteFile_byteExact sends 8 MiB of a file, head first, byte-exact, the
// refusals while it is in flight LoopOutputFull, OnWritable once at its end.
func TestWriteFile_byteExact(t *testing.T) {
	const size = 8 << 20
	eachBackend(t, func(t *testing.T, b backendCase) {
		f := tempFile(t, size)
		h := &fileH{fd: int(f.Fd()), head: []byte("HEAD")}
		_, addr := serve(t, b, h, nil)
		c := dialT(t, addr)
		fmt.Fprintf(c, "FILE %d\n", size)
		got, err := io.ReadAll(c)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		want := append([]byte("HEAD"), pattern(size)...)
		if !bytes.Equal(got, want) {
			t.Fatalf("got %d bytes, want %d (first difference at %d)", len(got), len(want), firstDiff(got, want))
		}
		if v := h.refused.Load(); v != nil {
			t.Fatal(v)
		}
		if n := h.writable.Load(); n != 1 {
			t.Fatalf("OnWritable fired %d times, want once", n)
		}
	})
}

func firstDiff(a, b []byte) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// TestWriteFile_truncated: a file shorter than its promise closes the
// connection on what it held, with LoopFileTruncated.
func TestWriteFile_truncated(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		f := tempFile(t, 4096)
		h := &fileH{fd: int(f.Fd()), head: []byte("HEAD")}
		_, addr := serve(t, b, h, nil)
		c := dialT(t, addr)
		fmt.Fprintf(c, "FILE %d\n", 8<<20)
		got, err := io.ReadAll(c)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if want := append([]byte("HEAD"), pattern(4096)...); !bytes.Equal(got, want) {
			t.Fatalf("got %d bytes, want %d", len(got), len(want))
		}
		deadline := time.Now().Add(5 * time.Second)
		for h.closeErr.Load() == nil && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if v := h.closeErr.Load(); v == nil || v.(string) != corenet.LoopFileTruncated.Error() {
			t.Fatalf("close err = %v, want %v", v, corenet.LoopFileTruncated)
		}
	})
}

// TestWriteFile_midCloseLeaksNothing closes a client in the middle of a file:
// the descriptor table is back where it was — the duplicate and the pipe
// gone, whatever the pipe held — on each backend.
func TestWriteFile_midCloseLeaksNothing(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		f := tempFile(t, 8<<20)
		h := &fileH{fd: int(f.Fd()), head: []byte("HEAD")}
		_, addr := serve(t, b, h, nil)
		before := fdCount(t)
		for range 5 {
			c := dialT(t, addr)
			fmt.Fprintf(c, "FILE %d\n", 8<<20)
			buf := make([]byte, 128<<10)
			if _, err := io.ReadFull(c, buf); err != nil {
				t.Fatalf("read: %v", err)
			}
			if tc, ok := c.(*stdnet.TCPConn); ok {
				_ = tc.SetLinger(0)
			}
			_ = c.Close()
		}
		deadline := time.Now().Add(10 * time.Second)
		for fdCount(t) > before && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if after := fdCount(t); after > before {
			t.Fatalf("descriptors: %d before, %d after five closed clients", before, after)
		}
	})
}
