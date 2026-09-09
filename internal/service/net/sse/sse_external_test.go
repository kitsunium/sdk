// Package sse_test — the event stream as a handler drives it.
package sse_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/sse"
)

// TestNewRefusesAResponseThatCannotFlush is the streaming contract itself.
//
// A stream that cannot flush is not a slow stream, it is a broken one: every
// frame sits in the transport buffer until the handler returns, and an event
// stream's handler by nature does not return. The client waits forever on a
// connection the server believes is working. Refusing at construction is the
// only point at which that is still fixable — and the response must come back
// untouched, so the handler can answer with an error of its own.
func TestNewRefusesAResponseThatCannotFlush(t *testing.T) {
	t.Parallel()
	w := &noFlush{header: make(http.Header)}
	stream, err := sse.New(w, httptest.NewRequest(http.MethodGet, "/stream", nil))
	if !errs.HasCode(err, corenet.CodeSSEFlushUnsupported) {
		t.Fatalf("New() = %v, want SSE_FLUSH_UNSUPPORTED", err)
	}
	if stream != nil {
		t.Fatalf("New() returned a stream alongside its error")
	}
	if len(w.header) != 0 {
		t.Fatalf("New() set %v before refusing; the response must be left untouched", w.header)
	}
	if w.written {
		t.Fatalf("New() committed the response before refusing")
	}
}

// TestNewAcceptsAFlusherBehindAWrapper pins that the probe walks the same
// Unwrap chain http.ResponseController does. A middleware that wraps the
// ResponseWriter to count bytes is the normal shape of an HTTP stack, and
// refusing to stream through one would make the package unusable in exactly
// the servers that need it.
func TestNewAcceptsAFlusherBehindAWrapper(t *testing.T) {
	t.Parallel()
	inner := newCapture()
	stream, err := sse.New(&wrapper{ResponseWriter: inner}, httptest.NewRequest(http.MethodGet, "/stream", nil))
	if err != nil {
		t.Fatalf("New() = %v, want nil through a wrapper", err)
	}
	defer closeStream(t, stream)
	if inner.flushCount() == 0 {
		t.Fatalf("New() never flushed through the wrapper")
	}
}

// TestNewOpensTheStreamImmediately pins that the headers and the flush happen
// at construction, not at the first event. On a stream whose first event may be
// minutes away, that is the difference between a client that has connected and
// one that looks hung.
func TestNewOpensTheStreamImmediately(t *testing.T) {
	t.Parallel()
	w := newCapture()
	stream, err := sse.New(w, httptest.NewRequest(http.MethodGet, "/stream", nil), sse.WithoutKeepAlive())
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	defer closeStream(t, stream)
	type tc struct {
		name   string
		header string
		want   string
	}
	tests := []tc{
		{name: "the media type is the protocol handshake", header: "Content-Type", want: "text/event-stream"},
		{name: "a cached event stream never updates", header: "Cache-Control", want: "no-cache"},
		{name: "nginx buffers proxied responses by default", header: "X-Accel-Buffering", want: "no"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := w.Header().Get(c.header); got != c.want {
				t.Fatalf("%s = %q, want %q", c.header, got, c.want)
			}
		})
	}
	if w.status() != http.StatusOK {
		t.Errorf("status = %d, want 200", w.status())
	}
	if w.flushCount() == 0 {
		t.Errorf("New() did not flush the headers")
	}
}

// TestNewKeepsAHeaderTheCallerAlreadySet pins that only the media type is
// imposed. Content-Type IS the protocol, so it is not negotiable; the other two
// are advisory, and a caller who set one on purpose knows something about its
// deployment that the SDK does not.
func TestNewKeepsAHeaderTheCallerAlreadySet(t *testing.T) {
	t.Parallel()
	w := newCapture()
	w.Header().Set("Cache-Control", "no-store")
	stream, err := sse.New(w, httptest.NewRequest(http.MethodGet, "/stream", nil), sse.WithoutKeepAlive())
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	defer closeStream(t, stream)
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want the caller's own no-store", got)
	}
}

// TestStreamSendsWholeFrames pins the handler-visible surface: an event, a
// comment, and the frames that reach the wire in order.
func TestStreamSendsWholeFrames(t *testing.T) {
	t.Parallel()
	w := newCapture()
	stream, err := sse.New(w, httptest.NewRequest(http.MethodGet, "/stream", nil), sse.WithoutKeepAlive())
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	defer closeStream(t, stream)
	if serr := stream.Send(corenet.SSEEventValue{ID: "1", Name: "tick", Data: "one\ntwo"}); serr != nil {
		t.Fatalf("Send() = %v, want nil", serr)
	}
	if cerr := stream.Comment("still here"); cerr != nil {
		t.Fatalf("Comment() = %v, want nil", cerr)
	}
	want := "id: 1\nevent: tick\ndata: one\ndata: two\n\n: still here\n\n"
	if got := w.body(); got != want {
		t.Fatalf("stream wrote %q, want %q", got, want)
	}
	//: one flush at construction plus one per frame — an unflushed frame is a
	//: frame the client does not have.
	if got := w.flushCount(); got != 3 {
		t.Fatalf("flushes = %d, want 3 (headers, event, comment)", got)
	}
}

// TestStreamRefusesAnUnrepresentableEventWithoutWriting pins that a refusal
// costs the stream nothing. The peer is already parsing what came before, so
// half a frame is not a recoverable error — it is a corrupt stream.
func TestStreamRefusesAnUnrepresentableEventWithoutWriting(t *testing.T) {
	t.Parallel()
	w := newCapture()
	stream, err := sse.New(w, httptest.NewRequest(http.MethodGet, "/stream", nil), sse.WithoutKeepAlive())
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	defer closeStream(t, stream)
	if serr := stream.Send(corenet.SSEEventValue{ID: "a\nb", Data: "x"}); !errs.HasCode(serr, corenet.CodeSSEFieldInvalid) {
		t.Fatalf("Send() = %v, want SSE_FIELD_INVALID", serr)
	}
	if got := w.body(); got != "" {
		t.Fatalf("a refused event wrote %q, want nothing", got)
	}
	//: the stream survives a refusal — one bad event is not a dead peer.
	if serr := stream.Send(corenet.SSEEventValue{Data: "fine"}); serr != nil {
		t.Fatalf("Send() after a refusal = %v, want the stream still open", serr)
	}
}

// TestStreamEnds pins the three ways a stream ends, and pins that all three
// look identical to a handler: Done closes and Send refuses with the same code.
//
// The drain case is the one this package was written for. Without it an endless
// stream holds a graceful shutdown open until the budget expires and the socket
// is severed under it — on every deploy, for every connected client.
func TestStreamEnds(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: end triggers the ending under test.
		end func(cancel context.CancelFunc, draining chan struct{}, stream *sse.Stream)
	}
	tests := []tc{
		{
			name: "the client goes away",
			end:  func(cancel context.CancelFunc, _ chan struct{}, _ *sse.Stream) { cancel() },
		},
		{
			name: "the server begins draining",
			end:  func(_ context.CancelFunc, draining chan struct{}, _ *sse.Stream) { close(draining) },
		},
		{
			name: "the handler closes it",
			end: func(_ context.CancelFunc, _ chan struct{}, stream *sse.Stream) {
				if err := stream.Close(); err != nil {
					panic(err)
				}
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		draining := make(chan struct{})
		req := httptest.NewRequest(http.MethodGet, "/stream", nil).
			WithContext(corenet.WithDrainSignal(ctx, draining))
		stream, err := sse.New(newCapture(), req, sse.WithoutKeepAlive())
		if err != nil {
			t.Fatalf("New() = %v, want nil", err)
		}
		defer closeStream(t, stream)
		select {
		case <-stream.Done():
			t.Fatalf("the stream was already over before anything ended it")
		default:
		}
		c.end(cancel, draining, stream)
		select {
		case <-stream.Done():
		case <-time.After(2 * time.Second):
			t.Fatalf("Done() never closed")
		}
		//: a handler that only ever calls Send must terminate too, or the
		//: signal is advisory and the drain is still held open.
		if serr := stream.Send(corenet.SSEEventValue{Data: "late"}); !errs.HasCode(serr, corenet.CodeSSEStreamClosed) {
			t.Fatalf("Send() after the end = %v, want SSE_STREAM_CLOSED", serr)
		}
		if cerr := stream.Comment("late"); !errs.HasCode(cerr, corenet.CodeSSEStreamClosed) {
			t.Fatalf("Comment() after the end = %v, want SSE_STREAM_CLOSED", cerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestKeepAliveProvesLivenessWhileNothingHappens pins the reason keep-alive
// exists: an idle stream is indistinguishable from a dead one to a proxy
// counting idle seconds, and a comment is the only traffic that proves
// otherwise without being mistaken for an event.
func TestKeepAliveProvesLivenessWhileNothingHappens(t *testing.T) {
	t.Parallel()
	w := newCapture()
	stream, err := sse.New(w, httptest.NewRequest(http.MethodGet, "/stream", nil),
		sse.KeepAlive(10*time.Millisecond))
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	defer closeStream(t, stream)
	deadline := time.Now().Add(3 * time.Second)
	//: two comments prove a cadence rather than a single opening write.
	for strings.Count(w.body(), ":") < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("keep-alive wrote %q, want repeated comments", w.body())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if strings.Contains(w.body(), "data:") {
		t.Fatalf("keep-alive emitted a data line: %q — a comment can never be mistaken for an event", w.body())
	}
}

// TestWithoutKeepAliveIsSilent pins the explicit opt-out. It is the counterpart
// of the zero-value clamp: "never" must be something a caller writes on
// purpose, and when they do it must actually be never.
func TestWithoutKeepAliveIsSilent(t *testing.T) {
	t.Parallel()
	w := newCapture()
	stream, err := sse.New(w, httptest.NewRequest(http.MethodGet, "/stream", nil), sse.WithoutKeepAlive())
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	defer closeStream(t, stream)
	time.Sleep(50 * time.Millisecond)
	if got := w.body(); got != "" {
		t.Fatalf("a disabled keep-alive wrote %q, want nothing", got)
	}
}

// TestNewRefusesAnInertOption pins ADR 0031's refusal half: a negative interval
// is neither a shorter one nor "never", and any reading the SDK picked for it
// would be invented intent.
func TestNewRefusesAnInertOption(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		opt  sse.Option
		code errs.Code
	}
	tests := []tc{
		{name: "a negative keep-alive", opt: sse.KeepAlive(-time.Second), code: corenet.CodeSSEStreamMisconfigured},
		{name: "a negative write budget", opt: sse.WriteTimeout(-time.Second), code: corenet.CodeSSEStreamMisconfigured},
		{name: "a negative retry hint", opt: sse.Retry(-time.Second), code: corenet.CodeSSEFieldInvalid},
		{name: "a retry hint the wire cannot carry", opt: sse.Retry(time.Microsecond), code: corenet.CodeSSEFieldInvalid},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		w := newCapture()
		stream, err := sse.New(w, httptest.NewRequest(http.MethodGet, "/stream", nil), c.opt)
		if !errs.HasCode(err, c.code) {
			t.Fatalf("New() = %v, want code %v", err, c.code)
		}
		if stream != nil {
			t.Fatalf("New() returned a stream alongside its error")
		}
		if len(w.Header()) != 0 {
			t.Fatalf("New() touched the response before refusing: %v", w.Header())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestRetryOpensWithTheReconnectionPolicy pins that the hint goes out first.
// A fleet that all reconnect on the browser's default of about three seconds
// is a thundering herd aimed at a server that has just restarted, and the
// opening frame is the server's one chance to say otherwise.
func TestRetryOpensWithTheReconnectionPolicy(t *testing.T) {
	t.Parallel()
	w := newCapture()
	stream, err := sse.New(w, httptest.NewRequest(http.MethodGet, "/stream", nil),
		sse.Retry(7*time.Second), sse.WithoutKeepAlive())
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	defer closeStream(t, stream)
	if got, want := w.body(), "retry: 7000\n\n"; got != want {
		t.Fatalf("opening frame = %q, want %q", got, want)
	}
}

// TestLastEventIDIsHandedToTheHandler pins the resume decision: the SDK reads
// the cursor and hands it over, and replays nothing. A buffer held by the
// stream would be empty at exactly the moment a resume needs it, because a
// reconnect is a new connection and therefore a new stream.
func TestLastEventIDIsHandedToTheHandler(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		header string
		want   string
	}
	tests := []tc{
		{name: "a reconnecting client", header: "cursor-42", want: "cursor-42"},
		{name: "a fresh connection", header: "", want: ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/stream", nil)
		if c.header != "" {
			req.Header.Set(corenet.SSELastEventIDHeader, c.header)
		}
		stream, err := sse.New(newCapture(), req, sse.WithoutKeepAlive())
		if err != nil {
			t.Fatalf("New() = %v, want nil", err)
		}
		defer closeStream(t, stream)
		if got := stream.LastEventID(); got != c.want {
			t.Fatalf("LastEventID() = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestConcurrentSendsStayWholeFrames pins the serialisation. Two interleaved
// frames are not two events, they are one corrupt one, and a handler fanning
// events in from several producers is the normal shape — as is the keep-alive,
// which shares the writer with all of them.
func TestConcurrentSendsStayWholeFrames(t *testing.T) {
	t.Parallel()
	const senders int = 8
	const each int = 25
	w := newCapture()
	stream, err := sse.New(w, httptest.NewRequest(http.MethodGet, "/stream", nil),
		sse.KeepAlive(time.Millisecond))
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	defer closeStream(t, stream)
	var wg sync.WaitGroup
	wg.Add(senders)
	for range senders {
		go func() {
			defer wg.Done()
			for range each {
				//: reported rather than discarded, but not fatal: a send that
				//: loses its race with the ending is not a framing failure,
				//: which is what this test is about.
				if serr := stream.Send(corenet.SSEEventValue{Data: "0123456789"}); serr != nil {
					t.Logf("send during the fan-in: %v", serr)
				}
			}
		}()
	}
	wg.Wait()
	for _, frame := range strings.SplitAfter(w.body(), "\n\n") {
		//: every frame is either one whole event, one whole comment, or the
		//: empty remainder after the final terminator.
		if frame == "" || frame == "data: 0123456789\n\n" || frame == ": keep-alive\n\n" {
			continue
		}
		t.Fatalf("interleaved frame on the wire: %q", frame)
	}
}

// closeStream closes a stream in a defer, failing the test if it reports a
// problem.
func closeStream(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

// capture is a concurrency-safe ResponseWriter. httptest.ResponseRecorder is
// not: the keep-alive writes from its own goroutine while the test reads, which
// is a genuine race the recorder would report rather than tolerate.
type capture struct {
	mu      sync.Mutex
	header  http.Header
	buf     bytes.Buffer
	code    int
	flushes int
}

// newCapture builds an empty capture.
func newCapture() *capture {
	//: the header map must exist before the first Set, exactly as net/http's own does.
	return &capture{header: make(http.Header)}
}

// Header implements http.ResponseWriter.
func (c *capture) Header() http.Header {
	//: the map itself, so a caller's Set is visible here.
	return c.header
}

// Write implements http.ResponseWriter.
func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: an implicit 200 on the first write, as net/http does.
	if c.code == 0 {
		c.code = http.StatusOK
	}
	//: bytes.Buffer never fails a write.
	return c.buf.Write(p)
}

// WriteHeader implements http.ResponseWriter.
func (c *capture) WriteHeader(code int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: the first status wins, as net/http does.
	if c.code == 0 {
		c.code = code
	}
}

// Flush implements http.Flusher.
func (c *capture) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: an implicit 200 on the first flush, as net/http does.
	if c.code == 0 {
		c.code = http.StatusOK
	}
	c.flushes++
}

// body returns what has been written so far.
func (c *capture) body() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: a copy, so the caller can read it while the keep-alive keeps writing.
	return c.buf.String()
}

// status returns the response status.
func (c *capture) status() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: whatever was written, or zero when nothing was.
	return c.code
}

// flushCount returns how many times the response has been flushed.
func (c *capture) flushCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: the count, read under the lock the keep-alive also takes.
	return c.flushes
}

// noFlush is a ResponseWriter that cannot stream — the case the constructor
// exists to refuse.
type noFlush struct {
	header  http.Header
	written bool
}

// Header implements http.ResponseWriter.
func (n *noFlush) Header() http.Header {
	//: the header map, so the test can prove nothing was set.
	return n.header
}

// Write implements http.ResponseWriter.
func (n *noFlush) Write(p []byte) (int, error) {
	n.written = true
	//: accepted and discarded; the test only asks whether it happened.
	return len(p), nil
}

// WriteHeader implements http.ResponseWriter.
func (n *noFlush) WriteHeader(int) {
	n.written = true
}

// wrapper is the shape of a middleware that decorates the response: it forwards
// everything and exposes what it wraps, which is how net/http expects a wrapper
// to cooperate with http.ResponseController.
type wrapper struct {
	http.ResponseWriter
}

// Unwrap exposes the wrapped writer to http.ResponseController.
func (w *wrapper) Unwrap() http.ResponseWriter {
	//: the controller — and our own probe — follow this to find the flusher.
	return w.ResponseWriter
}
