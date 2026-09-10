// Package sse_test — the event stream as a handler drives it.
package sse_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdnet "net"
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

// drainStreamCount is how many streams are held open across a drain. It is
// large enough that a per-stream cost would show and small enough to stay
// inside the default file-descriptor limit with a client connection each.
const drainStreamCount int = 64

// drainBudget is the shutdown budget the control case is allowed to burn. It is
// deliberately short: the control exists to prove the budget is what gets spent
// when nothing observes the drain, and proving it should not cost seconds.
const drainBudget time.Duration = 300 * time.Millisecond

// deadlineFrames is how many events the write-deadline tests send. Three is the
// smallest count that distinguishes "set once" from "refreshed per frame" and
// still leaves a frame after the injected failure.
const deadlineFrames int = 3

// errDeadlineRefused is what deadlineWriter reports once it is refusing. It is
// a test double's own error, not an SDK sentinel: the point of the branch under
// test is that the stream does not care WHICH error a deadline refusal is.
var errDeadlineRefused = errors.New("sse_test: the response cannot carry a write deadline")

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

// TestDrainWithOpenStreamsFinishesInMilliseconds is the executable form of a
// claim this package's CLAUDE.md has made in prose since it was written —
// "finishes in milliseconds instead of burning its whole budget" — and of
// ADR 0043's "40 ms, clean".
//
// The only guard that existed anywhere was pkg/v1/server's
// TestHTTPAdapterDrainsOnShutdown, which fails at three SECONDS with three
// streams open. A regression from 40 ms to 2.9 s would have passed it, and
// passed everything else in the repository. This measures the real number, over
// sixty-four streams, and asserts a bound an order of magnitude under that one.
//
// It also runs the CONTROL — the same server publishing no drain signal at all,
// which is the ADR 0043 defect itself — so the two numbers sit beside each
// other rather than the fast one standing alone.
//
// MUTATION-CHECKED. Replacing `draining := corenet.DrainSignal(ctx)` in
// Stream.watch with a nil channel — i.e. never observing the signal — turns the
// signalled case into the control and fails it at `shutdown = context deadline
// exceeded, want a clean drain` after `drain of 64 open streams: 300.2ms`. The
// budget, in full, which is exactly the behaviour ADR 0043 exists to prevent.
//
// A second mutation is worth recording because of HOW it failed rather than
// that it did: deleting the `s.end()` inside the `case <-draining:` arm — so the
// watcher observes the drain, returns, and tells nobody — does not fail this
// test, it HANGS it, and hangs the teardown too. Nothing is left to translate
// the request context's end into the stream's, so every handler blocks forever
// and httptest's own Close never returns. That is the shape a drain regression
// takes when the watcher is half-removed, and it is why the arm's two lines are
// not one.
func TestDrainWithOpenStreamsFinishesInMilliseconds(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: whether the server publishes a drain signal at all. False is the
		//: pre-ADR-0043 shape and must spend the whole budget.
		signalled bool
		//: whether Shutdown is expected to come back clean.
		wantClean bool
	}
	tests := []tc{
		{name: "the drain signal is published", signalled: true, wantClean: true},
		{name: "no drain signal — the ADR 0043 defect", signalled: false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		harness := newDrainHarness(t, c.signalled)
		defer harness.stop(t)
		harness.openStreams(t, drainStreamCount)

		//: the signal is closed exactly as the engine's adapter closes it, then
		//: the shutdown is timed from that instant.
		harness.beginDraining()
		ctx, cancel := context.WithTimeout(t.Context(), drainBudget)
		defer cancel()
		started := time.Now()
		err := harness.server.Config.Shutdown(ctx)
		elapsed := time.Since(started)
		t.Logf("drain of %d open streams: %v (signalled=%v, err=%v)", drainStreamCount, elapsed, c.signalled, err)

		//: the unsignalled control must burn the budget; that IS the defect.
		if !c.wantClean {
			//: anything faster would mean something else released the
			//: connections, and the comparison below would be meaningless.
			if err == nil {
				t.Fatalf("shutdown with no drain signal returned clean in %v; the control is not controlling anything", elapsed)
			}
			return
		}
		if err != nil {
			t.Fatalf("shutdown = %v, want a clean drain", err)
		}
		//: a third of the budget, against pkg/v1/server's three seconds. The
		//: measured value on this machine is two orders of magnitude under it.
		if elapsed > drainBudget/3 {
			t.Fatalf("drain of %d open streams took %v, want under %v", drainStreamCount, elapsed, drainBudget/3)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// drainHarness is a real http.Server serving real event streams over real
// connections. httptest is used rather than the SDK's own engine because the
// mechanism under test is the stream's reaction to the signal, and the engine
// lives in a package this one must not depend on.
type drainHarness struct {
	server   *httptest.Server
	draining chan struct{}
	clients  []*http.Client
	bodies   []io.Closer
	opened   chan struct{}
}

// newDrainHarness starts a server whose handler holds an event stream open
// until the stream itself says it is over.
func newDrainHarness(t *testing.T, signalled bool) *drainHarness {
	t.Helper()
	h := &drainHarness{
		draining: make(chan struct{}),
		opened:   make(chan struct{}, drainStreamCount),
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stream, err := sse.New(w, r, sse.WithoutKeepAlive())
		//: a stream that cannot open would leave the count short and hang the
		//: harness on its own wait, which is a clearer failure than a nil deref.
		if err != nil {
			return
		}
		defer closeStream(t, stream)
		//: one frame proves to the client that the stream is live before the
		//: drain begins; without it the client could still be in the headers.
		if serr := stream.Send(corenet.SSEEventValue{Data: "open"}); serr != nil {
			return
		}
		h.opened <- struct{}{}
		//: the handler holds the response open exactly as a real one does, and
		//: returns only when the stream ends. Before ADR 0043 nothing could end
		//: it, which is why Shutdown burned its budget.
		<-stream.Done()
	}))
	//: the engine publishes the signal on the LISTENER's base context, so every
	//: request derives from it; this mirrors http_adapter.go exactly.
	if signalled {
		srv.Config.BaseContext = func(stdnet.Listener) context.Context {
			//: one channel for every request this server serves.
			return corenet.WithDrainSignal(context.Background(), h.draining)
		}
	}
	srv.Start()
	h.server = srv
	return h
}

// openStreams opens n client connections and waits until every handler has
// written its first frame.
func (h *drainHarness) openStreams(t *testing.T, n int) {
	t.Helper()
	for range n {
		//: one client per connection: a shared transport would pool them and
		//: give us one connection however many requests we made.
		client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{}}
		h.clients = append(h.clients, client)
		resp, err := client.Get(h.server.URL + "/events")
		if err != nil {
			t.Fatalf("open stream: %v", err)
		}
		h.bodies = append(h.bodies, resp.Body)
	}
	//: every handler is now inside its wait, which is the state a drain has to
	//: get out of.
	for range n {
		<-h.opened
	}
}

// beginDraining closes the signal the server published, exactly as the engine's
// adapter does at the top of its shutdown.
func (h *drainHarness) beginDraining() {
	//: closed, never sent on, so every stream sees it and a late one sees it
	//: immediately.
	close(h.draining)
}

// stop releases the client connections and the server, in that order, so a
// control case whose handlers are still blocked can still be torn down.
func (h *drainHarness) stop(t *testing.T) {
	t.Helper()
	//: closing the bodies severs the connections, which ends the request
	//: contexts, which ends the streams the control case left blocked.
	for _, body := range h.bodies {
		//: a body already closed by the server is not a test failure, but a
		//: body that refuses to close is worth seeing in the log.
		if err := body.Close(); err != nil {
			t.Logf("closing a stream body: %v", err)
		}
	}
	//: idle connections are the transports', not the server's.
	for _, client := range h.clients {
		client.CloseIdleConnections()
	}
	h.server.Close()
}

// TestWriteDeadlineIsRefreshedOnEveryFrame covers a branch that, until this
// test, NOTHING in the repository executed.
//
// `httptest.ResponseRecorder` implements Flush but not SetWriteDeadline, and so
// does this package's own `capture` double — so every existing test built a
// stream with `deadlines` false and skipped the per-frame refresh entirely. The
// path is not incidental: it is the whole reason the stream replaces
// http.Server's response-wide WriteTimeout, which would otherwise cut every
// stream at a fixed instant. A benchmark measuring only the recorder harness
// understates a small send by 2.11× for the same reason (see BENCH.md).
//
// MUTATION-CHECKED. Hoisting the refresh out of write into New — "set it once",
// the change this design exists to refuse — fails it at `SetWriteDeadline was
// called 1 time(s), want 4 (one probe + one per frame)`.
func TestWriteDeadlineIsRefreshedOnEveryFrame(t *testing.T) {
	t.Parallel()
	w := &deadlineWriter{header: make(http.Header)}
	stream, err := sse.New(w, httptest.NewRequest(http.MethodGet, "/events", nil), sse.WithoutKeepAlive())
	if err != nil {
		t.Fatalf("New() = %v, want a stream", err)
	}
	defer closeStream(t, stream)
	//: the keep-alive is off, so every deadline after the probe is a frame's.
	for i := range deadlineFrames {
		if serr := stream.Send(corenet.SSEEventValue{Data: "tick"}); serr != nil {
			t.Fatalf("Send(%d) = %v, want nil", i, serr)
		}
	}
	//: one probe at construction plus one per frame.
	if got, want := len(w.deadlines), deadlineFrames+1; got != want {
		t.Fatalf("SetWriteDeadline was called %d time(s), want %d (one probe + one per frame)", got, want)
	}
	//: the probe is the ZERO time — that is how ResponseController is asked
	//: whether deadlines are supported without imposing one.
	if !w.deadlines[0].IsZero() {
		t.Errorf("the construction probe set %v, want the zero time", w.deadlines[0])
	}
	//: every later deadline is a real, strictly future bound, and each is later
	//: than the one before it — which is what "refreshed" means and what a
	//: single response-wide deadline could not do.
	for i, deadline := range w.deadlines[1:] {
		if deadline.IsZero() {
			t.Fatalf("frame %d set the zero deadline, want a bound", i)
		}
		//: a later frame must not inherit an earlier frame's expiry.
		if i > 0 && !deadline.After(w.deadlines[i]) {
			t.Errorf("frame %d's deadline %v is not after frame %d's %v", i, deadline, i-1, w.deadlines[i])
		}
	}
}

// TestAFailingWriteDeadlineDegradesRatherThanFailingTheFrame covers the other
// half of the same untested branch: `if derr := ...; derr != nil`.
//
// A deadline that cannot be set is not worth failing a frame over — the write
// below it reports any real problem — so the stream records that it cannot bound
// its writes and carries on. The alternative, failing the send, would kill a
// working stream over a bound it was applying as a courtesy.
//
// MUTATION-CHECKED, both halves. Returning the SetWriteDeadline error from write
// instead of clearing s.deadlines fails it at `Send after a refused deadline =
// [0.2.11.25 SSE_STREAM_CLOSED] The event stream is closed, want nil`. Dropping
// the `s.deadlines = false` assignment while still swallowing the error fails it
// at `the stream kept asking after a refusal: 4 call(s), want 2` — the branch
// still degrades, but it pays for a call it already knows will fail on every
// frame for the rest of the stream's life.
func TestAFailingWriteDeadlineDegradesRatherThanFailingTheFrame(t *testing.T) {
	t.Parallel()
	w := &deadlineWriter{header: make(http.Header)}
	stream, err := sse.New(w, httptest.NewRequest(http.MethodGet, "/events", nil), sse.WithoutKeepAlive())
	if err != nil {
		t.Fatalf("New() = %v, want a stream", err)
	}
	defer closeStream(t, stream)
	//: the first frame's deadline is refused, from here on.
	w.refuse()
	for range deadlineFrames {
		//: the frame must still go out; a refused deadline is not a refused
		//: write.
		if serr := stream.Send(corenet.SSEEventValue{Data: "tick"}); serr != nil {
			t.Fatalf("Send after a refused deadline = %v, want nil", serr)
		}
	}
	//: the probe, then exactly ONE refused attempt — after which the stream
	//: stops asking rather than paying for a call it knows will fail.
	if got := len(w.deadlines); got != 2 {
		t.Fatalf("the stream kept asking after a refusal: %d call(s), want 2", got)
	}
	//: and every frame is on the wire, which is the point.
	if got, want := strings.Count(w.body(), "data: tick"), deadlineFrames; got != want {
		t.Errorf("%d frame(s) reached the wire, want %d", got, want)
	}
}

// deadlineWriter is the response shape a real socket has: it flushes AND it
// accepts a write deadline. It exists because neither httptest.ResponseRecorder
// nor this file's `capture` does the second, so without it the per-frame
// deadline path has no test and no benchmark.
type deadlineWriter struct {
	mu        sync.Mutex
	header    http.Header
	buf       bytes.Buffer
	deadlines []time.Time
	refusing  bool
}

// Header implements http.ResponseWriter.
func (w *deadlineWriter) Header() http.Header {
	//: the map itself, so a caller's Set is visible here.
	return w.header
}

// Write implements http.ResponseWriter.
func (w *deadlineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	//: bytes.Buffer never fails a write, so a failure here would be the SDK's.
	return w.buf.Write(p)
}

// WriteHeader implements http.ResponseWriter.
func (w *deadlineWriter) WriteHeader(int) {}

// Flush implements http.Flusher, which is the streaming contract.
func (w *deadlineWriter) Flush() {}

// SetWriteDeadline implements the interface http.ResponseController looks for,
// recording every deadline it is handed so the test can assert on the sequence.
func (w *deadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadlines = append(w.deadlines, deadline)
	//: once refusing, every later call fails — which is what a socket whose
	//: connection has gone does.
	if w.refusing {
		//: the refusal a real ResponseWriter reports when it cannot bound.
		return errDeadlineRefused
	}
	//: accepted.
	return nil
}

// refuse makes every later SetWriteDeadline fail.
func (w *deadlineWriter) refuse() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refusing = true
}

// body returns what has reached the wire.
func (w *deadlineWriter) body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	//: a copy, so the caller cannot race the stream's own writes.
	return w.buf.String()
}
