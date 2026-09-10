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
	"runtime"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// allocRuns is how many frames each claim writes inside the measured window. A
// per-frame regression shows up as allocRuns allocations and an amortised one —
// a buffer doubling on a growth step — as four or five. Both are non-zero,
// which is the only property the assertions need; neither survives the integer
// division AllocsPerRun ends in.
const allocRuns int = 200

// allocWarmup is how many frames are written before the measurement starts. The
// claim is about the STEADY state — the first frame of a given size legitimately
// grows the buffer, and a test that measured it would be pinning the opposite of
// the contract.
const allocWarmup int = 8

// allocRuntimeWarmup is how many frames allocStream writes on a THROWAWAY
// stream before it builds the one under test. It is not about this package's
// buffer at all — that is allocWarmup's job, and eight frames do it. This one
// warms the GO RUNTIME.
//
// http.ResponseController.Flush, which every frame goes through, is a type
// switch over non-empty interfaces. The runtime serves those from a per-call-
// site cache it builds LAZILY and on purpose: runtime/iface.go gates the build
// behind `cheaprand()&1023 != 0`, so roughly one miss in 1024 pays for the
// cache and buildInterfaceSwitchCache allocates it. The result is a single
// ~64-byte allocation landing at an unpredictable point about a thousand calls
// into the process, attributable to no line in this repository.
//
// It is invisible to testing.AllocsPerRun — 1 over 200 is 0 after the integer
// division — and it is exactly what a total-counting window sees: one stray
// allocation in 4 of 10 otherwise-clean runs, before this constant existed.
// Thirty thousand frames put the probability that the cache is still unbuilt at
// (1023/1024)^30000, about 2e-13; measured, 0 strays in 300 consecutive
// windows. It costs about 30 ms and buys a guard that does not flake.
//
// It happens on a THROWAWAY stream, and that placement is load-bearing rather
// than tidy. The cache being warmed is the runtime's, per call site and
// process-global, so any stream can pay for it — while the state this file
// exists to police is PER STREAM. Warming thirty thousand frames through the
// stream under test would leave any accumulating regression far past its own
// growth steps: a slice that has doubled to 65 536 entries next grows at
// 131 072, so a 200-frame window would contain a growth step about 0.3 % of the
// time. That is the same blindness AllocsPerRun's integer division produces,
// reintroduced by the harness, and this is where it would have gone unnoticed.
const allocRuntimeWarmup int = 30000

// mallocsOver reports the TOTAL heap allocations f performs across runs calls,
// rather than the per-call average testing.AllocsPerRun reports.
//
// A stream is where the difference bites hardest, because the whole subject of
// this file is a buffer that GROWS. retain's job is to decide when s.frame is
// kept and when it is replaced, and every way of getting that decision wrong —
// a per-stream history of frame sizes, an id ring for reconnection, a growth
// policy that reallocates on a doubling schedule instead of on a threshold —
// costs an allocation on some sends and not on the ones between them.
// AllocsPerRun ends in `float64(mallocs / uint64(runs))`, an INTEGER division
// documented in the stdlib as being there so a caller can write `== 1` instead
// of `< 2`, so anything under one allocation per frame reports exactly 0.0. A
// stream leaking 8 allocations every 200 frames — 40 a second at a 5 Hz
// keep-alive, per stream, forever — is invisible to it.
//
// A total is not subject to that rounding. The bookkeeping mirrors
// AllocsPerRun's otherwise — pin GOMAXPROCS so no other P allocates into the
// count, warm up so lazily-initialised state is not attributed to the loop, and
// read the counter either side. There is deliberately NO runtime.GC(): an
// explicit collection returns before its sweep finishes, so the residual work
// allocates INSIDE the window; AllocsPerRun does not call it either, for the
// same reason.
func mallocsOver(runs int, f func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	//: warm up so first-call initialisation is not counted as steady state.
	f()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		f()
	}
	runtime.ReadMemStats(&after)
	//: Mallocs is cumulative and monotonic, so the difference is the total.
	return after.Mallocs - before.Mallocs
}

// TestSteadyStateSendAllocatesNothing pins initialFrameCapacity's claim, for a
// payload that fits inside the retention ceiling.
//
// MUTATION-CHECKED, twice, and the second is the one this file was rewritten
// for. Replacing `event.AppendTo(s.frame[:0])` with `event.AppendTo(nil)` — the
// shape the buffer exists to avoid — is a PER-FRAME defect and fails under
// either form. Giving Stream a `sent []int` field and appending len(frame) to
// it inside write — the shape of every "how many bytes has this stream sent?"
// instrument anyone would add — is an AMORTISED one, and it fails here at
// `200 Sends performed 4 allocations, want 0` while testing.AllocsPerRun
// reported 0 for the very same path, every run. Four, not two hundred: the
// slice doubles, so it allocates on the growth steps and not on the frames
// between them — which is precisely the shape AllocsPerRun's integer division
// rounds away. See mallocsOver.
func TestSteadyStateSendAllocatesNothing(t *testing.T) {
	stream, w := allocStream(t)
	defer allocClose(t, stream)
	event := corenet.SSEEventValue{ID: "42", Name: "tick", Data: strings.Repeat("x", 512)}
	//: the buffer reaches its high-water mark here, not inside the measurement.
	for range allocWarmup {
		allocSend(t, stream, event)
	}
	got := mallocsOver(allocRuns, func() {
		allocSend(t, stream, event)
	})
	if got != 0 {
		t.Fatalf("%d Sends performed %d allocations, want 0", allocRuns, got)
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
// MUTATION-CHECKED, twice. Replacing `corenet.AppendSSEComment(s.frame[:0],
// text)` with `corenet.AppendSSEComment(nil, text)` is the per-frame defect and
// fails under either form. The amortised one — a `sent []int` field on Stream
// appended to inside write — fails here at
// `200 Comments performed 4 allocations, want 0` and was reported as 0 by
// testing.AllocsPerRun, every run. This arm is the one that matters most: a
// server holding ten thousand idle streams would be leaking those four per
// stream per two hundred keep-alives, forever, and no lane would have said so.
// See mallocsOver.
func TestSteadyStateCommentAllocatesNothing(t *testing.T) {
	stream, w := allocStream(t)
	defer allocClose(t, stream)
	//: the buffer reaches its high-water mark here, not inside the measurement.
	for range allocWarmup {
		allocComment(t, stream)
	}
	got := mallocsOver(allocRuns, func() {
		allocComment(t, stream)
	})
	if got != 0 {
		t.Fatalf("%d Comments performed %d allocations, want 0", allocRuns, got)
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
// MUTATION-CHECKED, twice. Dropping `&& len(frame) < cap(frame)/2` from retain
// is the per-frame defect and fails under either form. The amortised one — a
// `sent []int` field on Stream appended to inside write — fails here at
// `200 outsized Sends in a steady stream of them performed 4 allocations,
// want 0` and was reported as 0 by testing.AllocsPerRun every time. See
// mallocsOver.
func TestOutsizedFramesInARowAllocateNothingEither(t *testing.T) {
	stream, _ := allocStream(t)
	defer allocClose(t, stream)
	event := corenet.SSEEventValue{Data: strings.Repeat("x", 4*maxRetainedFrameCapacity)}
	//: the buffer reaches its high-water mark here, not inside the measurement.
	for range allocWarmup {
		allocSend(t, stream, event)
	}
	got := mallocsOver(allocRuns, func() {
		allocSend(t, stream, event)
	})
	if got != 0 {
		t.Fatalf("%d outsized Sends in a steady stream of them performed %d allocations, want 0", allocRuns, got)
	}
}

// allocStream opens a stream on a discarding response, with the keep-alive off
// and both the runtime's and the stream's lazy state already spent.
//
// runtime.MemStats.Mallocs is a PROCESS-wide counter, so anything that
// allocates once, late, and off this package's call graph lands in the window
// anyway. Two such things exist here and both were found by counting totals,
// because a per-call average rounds a single stray to zero:
//
//   - The runtime's interface-switch cache, built lazily about a thousand
//     frames in. That is allocRuntimeWarmup's subject; see its comment.
//   - The goroutines a Stream owns. The keep-alive's first act is
//     time.NewTicker, and under GOMAXPROCS(1) it does not run until the
//     measuring goroutine yields — which mallocsOver first does at its opening
//     ReadMemStats, i.e. INSIDE the window. WithoutKeepAlive removes it
//     entirely, which is also right on its own terms: a keep-alive firing
//     mid-window would write a frame from another goroutine through the very
//     buffer under measurement. The comment path is still exercised, because
//     the tests call Comment directly — which is all the keep-alive does.
func allocStream(t *testing.T) (*Stream, *allocWriter) {
	t.Helper()
	//: the runtime's lazy state is spent elsewhere, before this stream exists.
	allocWarmRuntime(t)
	return allocNewStream(t)
}

// allocWarmRuntime spends the runtime's lazy interface-switch cache on a stream
// nothing will measure, so the stream under test starts with its own state
// fresh. See allocRuntimeWarmup for what is being warmed and why it may not
// happen on the stream that is about to be measured.
func allocWarmRuntime(t *testing.T) {
	t.Helper()
	stream, _ := allocNewStream(t)
	defer allocClose(t, stream)
	//: a tiny payload — this loop is about the runtime, not about bytes.
	warm := corenet.SSEEventValue{Data: "warm"}
	//: both verbs, because both reach write, where the type switch lives.
	for range allocRuntimeWarmup {
		allocSend(t, stream, warm)
		allocComment(t, stream)
	}
}

// allocNewStream is the bare construction allocStream and allocWarmRuntime
// share. It exists so the warm-up cannot recurse into the warm-up.
func allocNewStream(t *testing.T) (*Stream, *allocWriter) {
	t.Helper()
	w := &allocWriter{header: make(http.Header)}
	//: no keep-alive: see allocStream.
	stream, err := New(w, httptest.NewRequest(http.MethodGet, "/events", nil), WithoutKeepAlive())
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
