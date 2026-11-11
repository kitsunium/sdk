package sse_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/sse"
)

// fakeIO is a LoopIO recording each connection's bytes, closes and deadlines.
type fakeIO struct {
	mu       sync.Mutex
	out      map[corenet.LoopConnRef][]byte
	closed   map[corenet.LoopConnRef]bool
	deadline map[corenet.LoopConnRef]time.Duration
}

func newFakeIO() *fakeIO {
	return &fakeIO{out: map[corenet.LoopConnRef][]byte{}, closed: map[corenet.LoopConnRef]bool{}, deadline: map[corenet.LoopConnRef]time.Duration{}}
}

func (f *fakeIO) Write(ref corenet.LoopConnRef, p []byte) error {
	f.out[ref] = append(f.out[ref], p...)
	return nil
}
func (f *fakeIO) Close(ref corenet.LoopConnRef) error { f.closed[ref] = true; return nil }
func (f *fakeIO) SetDeadline(ref corenet.LoopConnRef, d time.Duration) error {
	f.deadline[ref] = d
	return nil
}
func (*fakeIO) Pressure(corenet.LoopConnRef) bool    { return false }
func (*fakeIO) Post(corenet.PostFunc, uintptr) error { return nil }
func (*fakeIO) Loop() int                            { return 0 }

// source accepts every request but /nope and records what opened.
type source struct {
	opened []sse.LoopStreamValue
	closed int
	last   []string
}

func (s *source) OpenSSE(req *corenet.HTTP1RequestValue, last []byte) int {
	s.last = append(s.last, string(last))
	if string(req.Path) == "/nope" {
		return 403
	}
	return 0
}
func (s *source) StreamOpened(st sse.LoopStreamValue)     { s.opened = append(s.opened, st) }
func (s *source) StreamClosed(sse.LoopStreamValue, error) { s.closed++ }

var ref1 = corenet.LoopConnRef{Slot: 1, Gen: 1}

// open drives one request through an http1 adapter over a LoopHandler.
func open(t testing.TB, src *source, raw string, opts ...sse.Option) (*http1.LoopHandler, *fakeIO) {
	t.Helper()
	h, err := sse.NewLoopHandler(src, opts...)
	if err != nil {
		t.Fatal(err)
	}
	a := http1.HandleLoop(h, http1.Parser{})
	f := newFakeIO()
	a.OnOpen(f, ref1)
	if n := a.OnData(f, ref1, []byte(raw)); n != len(raw) {
		t.Fatal(n)
	}
	a.OnBatchEnd(f)
	return a, f
}

const sseReq = "GET /events HTTP/1.1\r\nHost: h\r\nLast-Event-ID: 41\r\n\r\n"

func TestLoopSSE_headRetryAndEvents(t *testing.T) {
	src := &source{}
	_, f := open(t, src, sseReq, sse.Retry(2*time.Second), sse.KeepAlive(15*time.Second))
	if len(src.opened) != 1 || src.last[0] != "41" {
		t.Fatalf("opened %v, last %v", src.opened, src.last)
	}
	if err := src.opened[0].Send(corenet.SSEEventValue{ID: "42", Data: "hello"}); err != nil {
		t.Fatal(err)
	}
	wire := string(f.out[ref1])
	for _, want := range []string{"HTTP/1.1 200 OK\r\n", "Content-Type: text/event-stream\r\n", "Connection: close\r\n", "retry: 2000\n\n", "id: 42\ndata: hello\n\n"} {
		if !strings.Contains(wire, want) {
			t.Errorf("no %q in %q", want, wire)
		}
	}
	if strings.Contains(wire, "Content-Length") {
		t.Errorf("framed by length: %q", wire)
	}
	if f.deadline[ref1] != 15*time.Second {
		t.Errorf("keep-alive deadline %v", f.deadline[ref1])
	}
}

func TestLoopSSE_keepAliveCommentsAndRearm(t *testing.T) {
	src := &source{}
	a, f := open(t, src, sseReq, sse.KeepAlive(time.Second))
	f.deadline[ref1] = 0
	a.OnTimeout(f, ref1)
	if !strings.HasSuffix(string(f.out[ref1]), ":\n\n") {
		t.Fatalf("%q", f.out[ref1])
	}
	if f.deadline[ref1] != time.Second {
		t.Fatalf("not armed again: %v", f.deadline[ref1])
	}
}

func TestLoopSSE_withoutKeepAliveArmsNothing(t *testing.T) {
	src := &source{}
	_, f := open(t, src, sseReq, sse.WithoutKeepAlive())
	if _, armed := f.deadline[ref1]; armed {
		t.Fatalf("armed: %v", f.deadline[ref1])
	}
}

func TestLoopSSE_drainSendsRetryAndCloses(t *testing.T) {
	src := &source{}
	a, f := open(t, src, sseReq)
	a.OnDrain(f, ref1)
	if !strings.HasSuffix(string(f.out[ref1]), "retry: 3000\n\n") || !f.closed[ref1] {
		t.Fatalf("%q closed %v", f.out[ref1], f.closed[ref1])
	}
	a.OnClose(f, ref1, corenet.ServerClosed)
	if src.closed != 1 {
		t.Fatalf("StreamClosed %d", src.closed)
	}
}

func TestLoopSSE_programRefuses(t *testing.T) {
	src := &source{}
	_, f := open(t, src, "GET /nope HTTP/1.1\r\nHost: h\r\n\r\n")
	if len(src.opened) != 0 || !strings.HasPrefix(string(f.out[ref1]), "HTTP/1.1 403") {
		t.Fatalf("opened %v, %q", src.opened, f.out[ref1])
	}
}

func TestLoopSSE_refusals(t *testing.T) {
	if _, err := sse.NewLoopHandler(nil); err == nil {
		t.Fatal("a nil source was accepted")
	}
	if _, err := sse.NewLoopHandler(&source{}, sse.WriteTimeout(time.Second)); err == nil {
		t.Fatal("WriteTimeout was accepted")
	}
	if _, err := sse.NewLoopHandler(&source{}, sse.KeepAlive(-time.Second)); err == nil {
		t.Fatal("a negative keep-alive was accepted")
	}
}
