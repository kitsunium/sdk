//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"bytes"
	"fmt"
	"io"
	"sync/atomic"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestWriteFile_uringSplices sends a 64 KiB file on io_uring: the body leaves
// as IORING_OP_SPLICE SQEs, one IORING_OP_SEND carries only the head, and no
// IORING_OP_READ runs on the ring.
func TestWriteFile_uringSplices(t *testing.T) {
	var b backendCase
	for _, c := range backendsPresent() {
		if c.backend == corenet.LoopBackendIOURing {
			b = c
		}
	}
	if err := probeBackend(b.backend); err != nil {
		t.Skipf("io_uring not present: %v", err)
	}
	var sends, splices, reads atomic.Int64
	taken := func(op, _ uint8, _ uint32) {
		switch op {
		case opSend:
			sends.Add(1)
		case opSplice:
			splices.Add(1)
		case opRead:
			reads.Add(1)
		}
	}
	const size = 64 << 10
	f := tempFile(t, size)
	h := &fileH{fd: int(f.Fd()), head: []byte("HEAD")}
	_, addr := serve(t, b, h, func(c *Config) { c.hooks.taken = taken })
	c := dialT(t, addr)
	fmt.Fprintf(c, "FILE %d\n", size)
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if want := append([]byte("HEAD"), pattern(size)...); !bytes.Equal(got, want) {
		t.Fatalf("got %d bytes, want %d", len(got), len(want))
	}
	if n := splices.Load(); n < 2 {
		t.Fatalf("%d splice SQEs, want at least the linked pair", n)
	}
	if n := sends.Load(); n != 1 {
		t.Fatalf("%d send SQEs, want exactly the head's", n)
	}
	if n := reads.Load(); n != 0 {
		t.Fatalf("%d read SQEs of the file on the ring, want none", n)
	}
}
