//go:build linux && !race

// Package server — the cost of a file copy on a group with no bound (#970):
// io.Copy(c, f) reaches the socket through os.File's own sendfile, the
// handler's connection being a rawView, and allocates nothing once the
// connection asked its socket for the RawConn. Race off, in the alloc lane
// (tools/alloc-lane-targets.txt): the race detector allocates beside the copy.
package server

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestConnRawView_fileCopyAllocs pins #970's 0 B/op: a 1 MiB file copied with
// io.Copy(c, f) by a handler of a plaintext TCP group with no bound, the file
// rewound between copies, allocates nothing a copy. The first copy asks the
// socket for its RawConn, once a connection; the standard library's own
// *net.TCPConn asks it again each copy (one allocation), and the conn's
// ReadFrom, through net's sendfile, allocates four (BENCH.md).
func TestConnRawView_fileCopyAllocs(t *testing.T) {
	const size = 1 << 20
	path := filepath.Join(t.TempDir(), "file")
	created, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := errors.Join(created.Truncate(size), created.Close()); err != nil {
		t.Fatalf("size: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { swallowErr(f.Close()) })
	rounds := make(chan struct{})
	copied := make(chan error, 1)
	addr := serveOneConn(t, func(c corenet.Conn) error {
		//: a copy a round, the file rewound.
		for range rounds {
			_, err := f.Seek(0, io.SeekStart)
			//: rewound: the copy, a short one reported as a failure.
			if err == nil {
				var n int64
				n, err = io.Copy(c, f)
				//: short.
				if err == nil && n != size {
					err = io.ErrShortWrite
				}
			}
			copied <- err
		}
		//: the test ended.
		return nil
	})
	t.Cleanup(func() { close(rounds) })
	client := dialTimed(t, addr)
	buf := make([]byte, size)
	var failed error
	round := func() {
		rounds <- struct{}{}
		_, rerr := io.ReadFull(client, buf)
		cerr := <-copied
		//: the first failure kept; nothing allocated on success.
		if failed == nil && rerr != nil {
			failed = rerr
		}
		if failed == nil && cerr != nil {
			failed = cerr
		}
	}
	round()
	allocs := testing.AllocsPerRun(20, round)
	if failed != nil {
		t.Fatalf("a copy failed: %v", failed)
	}
	if allocs != 0 {
		t.Fatalf("io.Copy(c, f) allocated %v times a copy, want 0", allocs)
	}
}
