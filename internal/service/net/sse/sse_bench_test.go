// Package sse_test — what a stream costs to open, to hold and to write to.
package sse_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/sse"
)

// benchStreams is how many streams the footprint benchmark opens at once. It is
// large enough that the per-stream figures are not dominated by the runtime's
// own baseline and small enough to stay well inside this machine.
const benchStreams int = 2000

// benchOutsized is a payload comfortably past the retention ceiling. The
// ceiling itself is unexported, so the benchmark restates a size rather than
// the constant — and the two rows of BenchmarkOutsizedFrame are what would
// notice if they diverged, since a "repeated" row that started allocating would
// collapse onto the "alternating" one.
const benchOutsized int = 256 * 1024

// benchSizes are the payload lengths every per-frame row is measured at.
var benchSizes = []int{64, 256, 1024, 4096, 65536}

// errSink keeps a Send's verdict reachable so no call can be proven unused.
var errSink error

// benchWriter is the response an SSE stream is normally benchmarked against and
// it is the WRONG one on purpose — it flushes but cannot set a write deadline,
// exactly like httptest.ResponseRecorder and like the package's own test
// double. A stream built on it sets deadlines=false at construction and NEVER
// executes the per-frame deadline refresh, so a benchmark that used only this
// harness would price a path production does not take. Its twin is
// benchDeadlineWriter; BENCH.md publishes both rows side by side.
type benchWriter struct {
	header  http.Header
	written int64
}

// newBenchWriter builds a response that discards its body.
func newBenchWriter() *benchWriter {
	//: the header map must exist before the first Set, as net/http's own does.
	return &benchWriter{header: make(http.Header)}
}

// Header implements http.ResponseWriter.
func (w *benchWriter) Header() http.Header {
	//: the map itself, so a caller's Set is visible here.
	return w.header
}

// Write implements http.ResponseWriter by counting and discarding.
func (w *benchWriter) Write(p []byte) (int, error) {
	w.written += int64(len(p))
	//: a discard never fails, which is the point: the row is the SDK's cost.
	return len(p), nil
}

// WriteHeader implements http.ResponseWriter.
func (w *benchWriter) WriteHeader(int) {}

// Flush implements http.Flusher, which is the streaming contract.
func (w *benchWriter) Flush() {}

// benchDeadlineWriter is benchWriter plus SetWriteDeadline, which is what a
// real socket-backed ResponseWriter offers and what makes the stream take its
// per-frame deadline path. The deadline itself is accepted and dropped: the
// cost being priced is the refresh, not the kernel call it would reach.
type benchDeadlineWriter struct {
	benchWriter
	deadlines int64
}

// SetWriteDeadline implements the interface http.ResponseController looks for.
func (w *benchDeadlineWriter) SetWriteDeadline(time.Time) error {
	w.deadlines++
	//: accepted, so the stream keeps deadlines enabled for every later frame.
	return nil
}

// benchRequest builds the request a stream is opened on. It is built once and
// reused: New only reads the Last-Event-ID header and the context from it.
func benchRequest() *http.Request {
	//: an ordinary GET with no resume cursor — the fresh-connection shape.
	return httptest.NewRequest(http.MethodGet, "/events", nil)
}

// benchPayload builds a single-line payload of n bytes carrying no terminator,
// which is the overwhelmingly common event shape.
func benchPayload(n int) string {
	//: one repeated letter is enough; the frame encoder's cost is the scan and
	//: the copy, neither of which depends on the bytes.
	return strings.Repeat("x", n)
}

// benchLabel renders a size zero-padded so sub-benchmarks sort numerically.
func benchLabel(n int) string {
	label := strconv.Itoa(n)
	//: seven digits covers every size in the sweep.
	for len(label) < 7 {
		label = "0" + label
	}
	return label
}

// BenchmarkNewClose is the number an operator sizes a box with: what one open
// stream costs to set up and tear down, INCLUDING its goroutines. It is bounded
// below by two scheduler round trips, because Close joins both of them.
func BenchmarkNewClose(b *testing.B) {
	r := benchRequest()
	//: the default shape: watcher plus keep-alive.
	b.Run("with_keepalive", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			stream, err := sse.New(newBenchWriter(), r)
			//: a refusal here would silently measure nothing.
			if err != nil {
				b.Fatalf("New() = %v", err)
			}
			errSink = stream.Close()
		}
	})
	//: WithoutKeepAlive starts no pinger at all, so the delta is exactly what
	//: the second goroutine and its ticker cost.
	b.Run("without_keepalive", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			stream, err := sse.New(newBenchWriter(), r, sse.WithoutKeepAlive())
			//: a refusal here would silently measure nothing.
			if err != nil {
				b.Fatalf("New() = %v", err)
			}
			errSink = stream.Close()
		}
	})
}

// BenchmarkStreamFootprint is the other half of the sizing number, and it is
// the half a ns/op column cannot give: how much a stream costs while it is just
// SITTING THERE. An SSE server is defined by how many streams it holds open,
// not by its event rate, so this is the row that decides the box.
//
// It reports goroutines, heap bytes and STACK bytes per stream. Stack is
// separate on purpose: a goroutine's stack is not heap and never appears in a
// B/op column, so a benchmark that reported only allocations would understate
// an SSE server's memory by the larger of the two numbers.
func BenchmarkStreamFootprint(b *testing.B) {
	shapes := []struct {
		name string
		opts []sse.Option
	}{
		{name: "with_keepalive"},
		{name: "without_keepalive", opts: []sse.Option{sse.WithoutKeepAlive()}},
	}
	for _, shape := range shapes {
		b.Run(shape.name, func(b *testing.B) {
			r := benchRequest()
			//: one measured round; b.Loop keeps the harness honest about
			//: repeating it, and every round opens and closes the same count.
			for b.Loop() {
				b.StopTimer()
				before, beforeStack := benchMemory()
				beforeG := runtime.NumGoroutine()
				b.StartTimer()
				streams := make([]*sse.Stream, 0, benchStreams)
				//: open them all before measuring, so the figures are of a
				//: server at rest with benchStreams connections held.
				for range benchStreams {
					stream, err := sse.New(newBenchWriter(), r, shape.opts...)
					//: a refusal would silently measure nothing.
					if err != nil {
						b.Fatalf("New() = %v", err)
					}
					streams = append(streams, stream)
				}
				b.StopTimer()
				after, afterStack := benchMemory()
				afterG := runtime.NumGoroutine()
				b.ReportMetric(float64(afterG-beforeG)/float64(benchStreams), "goroutines/stream")
				b.ReportMetric(float64(after-before)/float64(benchStreams), "heapB/stream")
				b.ReportMetric(float64(afterStack-beforeStack)/float64(benchStreams), "stackB/stream")
				//: closing joins every goroutine, which is what makes the next
				//: round's baseline comparable to this one's.
				for _, stream := range streams {
					errSink = stream.Close()
				}
				b.StartTimer()
			}
		})
	}
}

// benchMemory returns live heap bytes and stack bytes, after a GC so the heap
// figure is what is RETAINED rather than what has been allocated.
func benchMemory() (heap, stack uint64) {
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	//: HeapAlloc after a GC is live data; StackInuse is the goroutine stacks,
	//: which the heap figure deliberately does not include.
	return stats.HeapAlloc, stats.StackInuse
}

// BenchmarkSend is the steady-state per-event cost, measured on BOTH response
// shapes because they take different code paths. The no_deadlines rows are what
// every existing test in this package would have measured; the deadlines rows
// are what production pays.
func BenchmarkSend(b *testing.B) {
	harnesses := []struct {
		name string
		make func() http.ResponseWriter
	}{
		{name: "no_deadlines", make: func() http.ResponseWriter { return newBenchWriter() }},
		{name: "deadlines", make: func() http.ResponseWriter { return &benchDeadlineWriter{benchWriter: *newBenchWriter()} }},
	}
	r := benchRequest()
	for _, h := range harnesses {
		for _, n := range benchSizes {
			b.Run(h.name+"/"+benchLabel(n), benchSendRow(h.make, r, benchPayload(n), n))
		}
	}
}

// benchSendRow returns one Send row. The payload and the harness travel as
// ARGUMENTS rather than as captured loop variables, so nothing about the
// sweep's shape puts either on the heap and the row prices the send alone.
func benchSendRow(makeWriter func() http.ResponseWriter, r *http.Request, payload string, n int) func(*testing.B) {
	event := corenet.SSEEventValue{Data: payload}
	//: one closure per row, over values that are already fixed.
	return func(b *testing.B) {
		stream := benchStream(b, makeWriter(), r)
		defer benchClose(b, stream)
		b.SetBytes(int64(n))
		b.ReportAllocs()
		for b.Loop() {
			errSink = stream.Send(event)
		}
	}
}

// BenchmarkComment prices the keep-alive frame in situ. Every idle stream emits
// one on a timer whether it has anything to say or not, so on a server holding
// many idle streams this is the only work there is.
func BenchmarkComment(b *testing.B) {
	r := benchRequest()
	//: the recorder shape, which never refreshes a deadline.
	b.Run("no_deadlines", func(b *testing.B) {
		stream := benchStream(b, newBenchWriter(), r)
		defer benchClose(b, stream)
		b.ReportAllocs()
		for b.Loop() {
			errSink = stream.Comment("keep-alive")
		}
	})
	//: the socket shape, which does.
	b.Run("deadlines", func(b *testing.B) {
		stream := benchStream(b, &benchDeadlineWriter{benchWriter: *newBenchWriter()}, r)
		defer benchClose(b, stream)
		b.ReportAllocs()
		for b.Loop() {
			errSink = stream.Comment("keep-alive")
		}
	})
}

// BenchmarkDrainToDone is ADR 0043's latency: from the server closing its drain
// channel to the stream having observed it. That is the per-stream part of a
// graceful shutdown's budget, and until now the only executable statement about
// it anywhere in the repository was a test that fails at three SECONDS.
func BenchmarkDrainToDone(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		draining := make(chan struct{})
		r := benchRequest().WithContext(corenet.WithDrainSignal(context.Background(), draining))
		stream := benchStream(b, newBenchWriter(), r)
		b.StartTimer()
		close(draining)
		<-stream.Done()
		b.StopTimer()
		errSink = stream.Close()
		b.StartTimer()
	}
}

// BenchmarkCloseJoin is the other half of a drain's cost: Close ends the stream
// and JOINS both goroutines, so a server that closes n streams pays this n
// times. The keep-alive row is the one that matters, because joining the pinger
// means waiting for it to come back around its select.
func BenchmarkCloseJoin(b *testing.B) {
	r := benchRequest()
	shapes := []struct {
		name string
		opts []sse.Option
	}{
		{name: "with_keepalive"},
		{name: "without_keepalive", opts: []sse.Option{sse.WithoutKeepAlive()}},
	}
	for _, shape := range shapes {
		b.Run(shape.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				stream := benchStream(b, newBenchWriter(), r, shape.opts...)
				b.StartTimer()
				errSink = stream.Close()
			}
		})
	}
}

// benchStream opens a stream or fails the benchmark.
func benchStream(b *testing.B, w http.ResponseWriter, r *http.Request, opts ...sse.Option) *sse.Stream {
	b.Helper()
	stream, err := sse.New(w, r, opts...)
	//: a refusal here would silently measure nothing at all.
	if err != nil {
		b.Fatalf("New() = %v, want a stream", err)
	}
	return stream
}

// benchClose closes a stream or fails the benchmark.
func benchClose(b *testing.B, stream *sse.Stream) {
	b.Helper()
	//: Close cannot fail today; asserting it keeps that true.
	if err := stream.Close(); err != nil {
		b.Fatalf("Close() = %v, want nil", err)
	}
}

// BenchmarkOutsizedFrame prices BOTH sides of the retention ceiling, which is
// the only honest way to publish it: the ceiling is not free, it is a trade, and
// the row that shows what it costs has to sit beside the row that shows what it
// buys.
//
// repeated    — every frame is outsized, so the buffer stays and nothing is
//
//	allocated. This is the workload a plain size cap punished.
//
// alternating — one outsized frame, then a small one. The small frame gives the
//
//	buffer back, and the next outsized frame re-grows it. This is
//	the price of not pinning 256 KiB per stream forever.
func BenchmarkOutsizedFrame(b *testing.B) {
	r := benchRequest()
	big := corenet.SSEEventValue{Data: benchPayload(benchOutsized)}
	small := corenet.SSEEventValue{Data: benchPayload(64)}
	//: the frames a stream working at this size actually sends.
	b.Run("repeated", func(b *testing.B) {
		stream := benchStream(b, newBenchWriter(), r)
		defer benchClose(b, stream)
		b.SetBytes(int64(benchOutsized))
		b.ReportAllocs()
		for b.Loop() {
			errSink = stream.Send(big)
		}
	})
	//: the one-off outsized frame the ceiling exists for, priced per PAIR so
	//: the re-growth is not hidden by averaging it over a long quiet stretch.
	b.Run("alternating", func(b *testing.B) {
		stream := benchStream(b, newBenchWriter(), r)
		defer benchClose(b, stream)
		b.SetBytes(int64(benchOutsized))
		b.ReportAllocs()
		for b.Loop() {
			errSink = stream.Send(big)
			errSink = stream.Send(small)
		}
	})
}
