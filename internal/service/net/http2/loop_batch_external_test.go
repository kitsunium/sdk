package http2_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// batchIO is a LoopIO with no engine behind it: it records what each
// connection was sent and whether it was closed.
type batchIO struct {
	out    map[corenet.LoopConnRef][]byte
	closed map[corenet.LoopConnRef]bool
}

func newBatchIO() *batchIO {
	return &batchIO{out: map[corenet.LoopConnRef][]byte{}, closed: map[corenet.LoopConnRef]bool{}}
}

func (f *batchIO) Write(ref corenet.LoopConnRef, p []byte) error {
	if f.closed[ref] {
		return corenet.LoopStaleRef
	}
	f.out[ref] = append(f.out[ref], p...)
	return nil
}

func (f *batchIO) Close(ref corenet.LoopConnRef) error {
	if f.closed[ref] {
		return corenet.LoopStaleRef
	}
	f.closed[ref] = true
	return nil
}

func (*batchIO) SetDeadline(corenet.LoopConnRef, time.Duration) error { return nil }
func (*batchIO) Pressure(corenet.LoopConnRef) bool                    { return false }
func (*batchIO) Post(corenet.PostFunc, uintptr) error                 { return nil }
func (*batchIO) Loop() int                                            { return 0 }

// deferH defers every answer and completes them in EndBatch, counting the
// calls: the port's contract is one call per loop iteration that served.
type deferH struct {
	ends    int
	pending []*corenet.HTTP1ResponseValue
}

func (h *deferH) ServeHTTP1(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	res.Defer = true
	h.pending = append(h.pending, res)
}

func (h *deferH) EndBatch(int) {
	h.ends++
	for _, res := range h.pending {
		res.Defer, res.Status, res.Body = false, 201, []byte("done")
	}
	h.pending = h.pending[:0]
}

// h2Request is a client preface, an empty SETTINGS and a GET of / on stream 1
// with END_STREAM, its field block every representation a plain literal.
func h2Request() []byte {
	b := []byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")
	b = http2.AppendFrameHeader(b, corehttp2.FrameValue{Type: corehttp2.FrameSettings})
	var block []byte
	for _, f := range [][2]string{{":method", "GET"}, {":scheme", "http"}, {":path", "/"}, {":authority", "h"}} {
		block = append(block, 0x00, byte(len(f[0])))
		block = append(block, f[0]...)
		block = append(block, byte(len(f[1])))
		block = append(block, f[1]...)
	}
	b = http2.AppendFrameHeader(b, corehttp2.FrameValue{
		Type: corehttp2.FrameHeaders, Flags: corehttp2.FlagEndStream | corehttp2.FlagEndHeaders, StreamID: 1, Length: len(block),
	})
	return append(b, block...)
}

// TestLoopHandler_oneEndBatchForBothVersions holds HTTP1Batcher's contract on
// a lane whose batch served an HTTP/1.1 and an HTTP/2 request: EndBatch is
// called once, and completes both deferred answers.
func TestLoopHandler_oneEndBatchForBothVersions(t *testing.T) {
	h := &deferH{}
	a := http2.HandleLoop(h, http1.Parser{})
	a.OnStart(1)
	io := newBatchIO()
	r1 := corenet.LoopConnRef{Slot: 1, Gen: 1}
	r2 := corenet.LoopConnRef{Slot: 2, Gen: 1}
	a.OnOpen(io, r1)
	a.OnOpen(io, r2)
	req1 := []byte("GET / HTTP/1.1\r\nHost: h\r\n\r\n")
	if n := a.OnData(io, r1, req1); n != len(req1) {
		t.Fatalf("HTTP/1.1 OnData consumed %d of %d", n, len(req1))
	}
	req2 := h2Request()
	if n := a.OnData(io, r2, req2); n != len(req2) {
		t.Fatalf("HTTP/2 OnData consumed %d of %d", n, len(req2))
	}
	a.OnBatchEnd(io)
	if h.ends != 1 {
		t.Fatalf("EndBatch called %d times for one batch serving both versions, want 1", h.ends)
	}
	if got := string(io.out[r1]); !strings.HasPrefix(got, "HTTP/1.1 201") {
		t.Fatalf("HTTP/1.1 answer = %q, want a 201", got)
	}
	if !hasFrame(io.out[r2], corehttp2.FrameHeaders, 1) || hasFrame(io.out[r2], corehttp2.FrameRSTStream, 1) {
		t.Fatalf("HTTP/2 answer on stream 1 missing or reset: % x", io.out[r2])
	}
	// A batch that served nothing calls nothing; one serving HTTP/2 alone, once.
	a.OnBatchEnd(io)
	if h.ends != 1 {
		t.Fatalf("an empty batch called EndBatch: %d", h.ends)
	}
	block := h2Request()[len("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")+corehttp2.FrameHeaderLen:]
	block = bytes.Clone(block)
	block[corehttp2.FrameHeaderLen-1] = 3 // stream 3
	if n := a.OnData(io, r2, block); n != len(block) {
		t.Fatalf("second HTTP/2 OnData consumed %d of %d", n, len(block))
	}
	a.OnBatchEnd(io)
	if h.ends != 2 {
		t.Fatalf("a batch serving HTTP/2 alone called EndBatch %d times, want 1", h.ends-1)
	}
}

// hasFrame reports whether b, a run of whole frames, holds one of typ on stream.
func hasFrame(b []byte, typ corehttp2.FrameType, stream uint32) bool {
	for len(b) >= corehttp2.FrameHeaderLen {
		fh, n, code := http2.ParseFrameHeader(b, 1<<24-1)
		if n == 0 || code != corehttp2.ErrCodeNoError || len(b) < corehttp2.FrameHeaderLen+fh.Length {
			return false
		}
		if fh.Type == typ && fh.StreamID == stream {
			return true
		}
		b = b[corehttp2.FrameHeaderLen+fh.Length:]
	}
	return false
}
