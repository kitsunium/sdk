//go:build !race

// Package sse — the allocation contract the package documents but nothing
// enforced.
//
// initialFrameCapacity's doc comment has claimed since the package was written
// that "a steady-state send allocates nothing", and until this file existed
// `grep -rn 'AllocsPerRun|Benchmark' internal/service/net/sse/` returned
// nothing at all. A documented allocation property with no gate is a claim, not
// a contract — and this one turned out to hold only with a qualifier the doc
// did not carry, namely that the buffer has reached its high-water mark. That
// qualifier is now in the constant's comment, beside the ceiling that bounds
// how high the mark may go.
//
// The `!race` constraint is not a preference: the race detector allocates
// shadow state on every memory access, so AllocsPerRun under `-race` measures
// the detector. That makes this file invisible to the race suite, which is why
// //internal/service/net/sse:sse_test carries an entry in
// tools/alloc-lane-targets.txt — the race-off alloc lane is its ONLY gate
// (SDK-wide rule 12).
package sse

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// allocRuns is how many times AllocsPerRun exercises each claim. It is well
// past the point where a per-frame allocation would round to zero: a single
// malloc on the path reports 1.0, not 1/allocRuns.
const allocRuns int = 200

// allocWarmup is how many frames are written before the measurement starts. The
// claim is about the STEADY state — the first frame of a given size legitimately
// grows the buffer, and a test that measured it would be pinning the opposite of
// the contract.
const allocWarmup int = 8

// TestSteadyStateSendAllocatesNothing pins initialFrameCapacity's claim, for a
// payload that fits inside the retention ceiling.
//
// MUTATION-CHECKED. Replacing `event.AppendTo(s.frame[:0])` with
// `event.AppendTo(nil)` — the shape the buffer exists to avoid — fails it at
// `Send allocated 4 times per frame, want 0`.
func TestSteadyStateSendAllocatesNothing(t *testing.T) {
	stream, w := allocStream(t)
	defer allocClose(t, stream)
	event := corenet.SSEEventValue{ID: "42", Name: "tick", Data: strings.Repeat("x", 512)}
	//: the buffer reaches its high-water mark here, not inside the measurement.
	for range allocWarmup {
		allocSend(t, stream, event)
	}
	got := testing.AllocsPerRun(allocRuns, func() {
		allocSend(t, stream, event)
	})
	if got != 0 {
		t.Fatalf("Send allocated %v times per frame, want 0", got)
	}
	//: a stream that wrote nothing would satisfy the claim trivially.
	if w.written == 0 {
		t.Fatal("nothing reached the wire; the measurement was of an empty path")
	}
}

// TestSteadyStateCommentAllocatesNothing pins the same property for the frame
// an IDLE stream emits. It matters more than Send's does: a server holding ten
// thousand idle streams writes nothing BUT keep-alive comments, so an
// allocation here is one per stream per interval, forever.
//
// MUTATION-CHECKED. Replacing `corenet.AppendSSEComment(s.frame[:0], text)`
// with `corenet.AppendSSEComment(nil, text)` fails it at `Comment allocated 2 times per
// frame, want 0`.
func TestSteadyStateCommentAllocatesNothing(t *testing.T) {
	stream, w := allocStream(t)
	defer allocClose(t, stream)
	//: the buffer reaches its high-water mark here, not inside the measurement.
	for range allocWarmup {
		allocComment(t, stream)
	}
	got := testing.AllocsPerRun(allocRuns, func() {
		allocComment(t, stream)
	})
	if got != 0 {
		t.Fatalf("Comment allocated %v times per frame, want 0", got)
	}
	//: a stream that wrote nothing would satisfy the claim trivially.
	if w.written == 0 {
		t.Fatal("nothing reached the wire; the measurement was of an empty path")
	}
}

// TestOutsizedFramesInARowAllocateNothingEither is the guard on the SECOND
// clause of retain's condition, and it exists because the first version of that
// condition did not have it.
//
// Releasing on size alone is the obvious reading of "bound the retention", and
// it is wrong for the one workload it hits: a stream whose every frame is
// outsized re-grows its buffer on every single send. Measured, that took a
// 64 KiB send from 6.6 µs to 25.5 µs — 3.89×, paid by exactly the traffic the
// ceiling was never aimed at. An outsized buffer is therefore kept while the
// frames still use it, and this pins that.
//
// MUTATION-CHECKED. Dropping `&& len(frame) < cap(frame)/2` from retain fails it
// at `an outsized Send in a steady stream of them allocated 2 times per frame,
// want 0`.
func TestOutsizedFramesInARowAllocateNothingEither(t *testing.T) {
	stream, _ := allocStream(t)
	defer allocClose(t, stream)
	event := corenet.SSEEventValue{Data: strings.Repeat("x", 4*maxRetainedFrameCapacity)}
	//: the buffer reaches its high-water mark here, not inside the measurement.
	for range allocWarmup {
		allocSend(t, stream, event)
	}
	got := testing.AllocsPerRun(allocRuns, func() {
		allocSend(t, stream, event)
	})
	if got != 0 {
		t.Fatalf("an outsized Send in a steady stream of them allocated %v times per frame, want 0", got)
	}
}

// allocStream opens a stream on a discarding response.
func allocStream(t *testing.T) (*Stream, *allocWriter) {
	t.Helper()
	w := &allocWriter{header: make(http.Header)}
	stream, err := New(w, httptest.NewRequest(http.MethodGet, "/events", nil))
	//: a refusal would leave the measurement with nothing to measure.
	if err != nil {
		t.Fatalf("New() = %v, want a stream", err)
	}
	return stream, w
}

// allocSend writes one event or fails the test.
func allocSend(t *testing.T, stream *Stream, event corenet.SSEEventValue) {
	t.Helper()
	//: a refused frame allocates differently from an accepted one.
	if err := stream.Send(event); err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
}

// allocComment writes one keep-alive comment or fails the test.
func allocComment(t *testing.T, stream *Stream) {
	t.Helper()
	//: a refused frame allocates differently from an accepted one.
	if err := stream.Comment(keepAliveComment); err != nil {
		t.Fatalf("Comment() = %v, want nil", err)
	}
}

// allocClose closes a stream or fails the test.
func allocClose(t *testing.T, stream *Stream) {
	t.Helper()
	//: Close cannot fail today; asserting it keeps that true.
	if err := stream.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

// allocWriter is a response that flushes and discards. It deliberately does NOT
// implement SetWriteDeadline: the deadline path calls time.Now, which does not
// allocate either, so leaving it out keeps this file measuring the encode and
// the write rather than the clock.
type allocWriter struct {
	header  http.Header
	written int64
}

// Header implements http.ResponseWriter.
func (w *allocWriter) Header() http.Header {
	//: the map itself, so a caller's Set is visible here.
	return w.header
}

// Write implements http.ResponseWriter by counting and discarding.
func (w *allocWriter) Write(p []byte) (int, error) {
	w.written += int64(len(p))
	//: a discard never fails, so the measurement is of the SDK's path only.
	return len(p), nil
}

// WriteHeader implements http.ResponseWriter.
func (w *allocWriter) WriteHeader(int) {}

// Flush implements http.Flusher, which is the streaming contract.
func (w *allocWriter) Flush() {}
