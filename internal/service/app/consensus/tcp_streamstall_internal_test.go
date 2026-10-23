package consensus

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// halfCloser is a net.Conn whose half-close does nothing: net.Pipe has
// none.
type halfCloser struct{ net.Conn }

func (halfCloser) CloseWrite() error { return nil }

// A snapshot stream to a receiver that stops reading after the offer had no
// deadline: the write blocked on the full window until the kernel gave up,
// about 15 min on Linux, the peer got no other snapshot and the transfer
// pinned compaction meanwhile (#594). Every write of the stream now fails
// within its own deadline. The receiver here is a conn that never reads.
func TestASnapshotStreamToAReceiverThatStopsReadingFails(t *testing.T) {
	local, remote := net.Pipe()
	defer func() { _ = local.Close(); _ = remote.Close() }()
	w := boundedWriter{conn: local, half: halfCloser{local}, d: 100 * time.Millisecond}
	open := func(int64) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(make([]byte, 1<<20))), nil
	}
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- streamFrom(w, open, 0) }()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("a stream to a receiver that stopped reading ended with %v, want its write deadline", err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("the stream took %v to fail, want about its 100 ms deadline", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stream to a receiver that stopped reading still blocked 5 s later")
	}
}

// A receiver that keeps reading, however slowly, is not cut: the deadline
// runs per write, not over the whole stream.
func TestASlowSnapshotStreamThatKeepsMovingSucceeds(t *testing.T) {
	local, remote := net.Pipe()
	defer func() { _ = local.Close(); _ = remote.Close() }()
	w := boundedWriter{conn: local, half: halfCloser{local}, d: 200 * time.Millisecond}
	const size = 8 << 15
	open := func(int64) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(make([]byte, size))), nil
	}
	go func() {
		buf := make([]byte, 1<<15)
		for {
			time.Sleep(50 * time.Millisecond)
			if _, err := remote.Read(buf); err != nil {
				return
			}
		}
	}()
	if err := streamFrom(w, open, 0); err != nil {
		t.Fatalf("a stream whose receiver kept reading failed: %v", err)
	}
}
