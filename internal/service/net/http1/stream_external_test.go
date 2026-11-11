package http1_test

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// streamH answers every request as a stream; the callbacks are recorded.
type streamH struct {
	opened  []corenet.HTTP1StreamValue
	timeout []corenet.HTTP1StreamValue
	drained []corenet.HTTP1StreamValue
	closed  []error
	onOpen  func(corenet.HTTP1StreamValue)
}

func (h *streamH) ServeHTTP1(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	res.Stream = true
	res.Header = []byte("Content-Type: text/plain\r\n")
}

func (h *streamH) OnStreamOpen(s corenet.HTTP1StreamValue) {
	h.opened = append(h.opened, s)
	if h.onOpen != nil {
		h.onOpen(s)
	}
}
func (h *streamH) OnStreamTimeout(s corenet.HTTP1StreamValue) { h.timeout = append(h.timeout, s) }
func (h *streamH) OnStreamDrain(s corenet.HTTP1StreamValue)   { h.drained = append(h.drained, s) }
func (h *streamH) OnStreamClose(_ corenet.HTTP1StreamValue, err error) {
	h.closed = append(h.closed, err)
}

func oneReq(t *testing.T, a *http1.LoopHandler, io *fakeIO, ref corenet.LoopConnRef, raw string) {
	t.Helper()
	a.OnOpen(io, ref)
	if n := a.OnData(io, ref, []byte(raw)); n != len(raw) {
		t.Fatal(n)
	}
	a.OnBatchEnd(io)
}

var streamReq = "GET /events HTTP/1.1\r\nHost: h\r\n\r\n"

func TestStream_headAndBody(t *testing.T) {
	h := &streamH{}
	a := http1.HandleLoop(h, http1.Parser{})
	fio := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	h.onOpen = func(s corenet.HTTP1StreamValue) {
		for range 1000 {
			if err := s.Send([]byte("event\n")); err != nil {
				t.Errorf("Send: %v", err)
			}
		}
		if err := s.End(); err != nil {
			t.Error(err)
		}
	}
	oneReq(t, a, fio, ref, streamReq)
	if len(h.opened) != 1 || h.opened[0].Ref != ref {
		t.Fatalf("opened %v", h.opened)
	}
	res, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(fio.out[ref])), &http.Request{Method: "GET"})
	if err != nil {
		t.Fatalf("%q: %v", fio.out[ref], err)
	}
	if res.ContentLength != -1 || !res.Close {
		t.Fatalf("framing: length %d, close %v", res.ContentLength, res.Close)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(body), "event\n"); got != 1000 {
		t.Fatalf("%d events, want 1000", got)
	}
	if !fio.closed[ref] {
		t.Fatal("End did not close the connection")
	}
}

func TestStream_pipelinedRequestsAreDropped(t *testing.T) {
	h := &streamH{}
	a := http1.HandleLoop(h, http1.Parser{})
	fio := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	a.OnOpen(fio, ref)
	raw := streamReq + "GET /second HTTP/1.1\r\nHost: h\r\n\r\n"
	if n := a.OnData(fio, ref, []byte(raw)); n != len(raw) {
		t.Fatalf("%d bytes taken", n)
	}
	a.OnBatchEnd(fio)
	if strings.Count(string(fio.out[ref]), "HTTP/1.1") != 1 {
		t.Fatalf("%q", fio.out[ref])
	}
	//: what follows the stream is read no more: the connection owes nothing.
	if n := a.OnData(fio, ref, []byte("more bytes")); n != 10 {
		t.Fatalf("%d", n)
	}
}

func TestStream_refusals(t *testing.T) {
	for _, c := range []struct {
		name, raw string
		mod       func(*corenet.HTTP1ResponseValue)
	}{
		{"HEAD", "HEAD / HTTP/1.1\r\nHost: h\r\n\r\n", nil},
		{"204", "GET / HTTP/1.1\r\nHost: h\r\n\r\n", func(r *corenet.HTTP1ResponseValue) { r.Status = 204 }},
		{"304", "GET / HTTP/1.1\r\nHost: h\r\n\r\n", func(r *corenet.HTTP1ResponseValue) { r.Status = 304 }},
		{"deferred", "GET / HTTP/1.1\r\nHost: h\r\n\r\n", func(r *corenet.HTTP1ResponseValue) { r.Defer = true }},
	} {
		app := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, r *corenet.HTTP1ResponseValue) {
			r.Stream = true
			if c.mod != nil {
				c.mod(r)
			}
		})
		a := http1.HandleLoop(streamingFunc{app}, http1.Parser{})
		fio := newFakeIO(0)
		ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
		oneReq(t, a, fio, ref, c.raw)
		if !strings.HasPrefix(string(fio.out[ref]), "HTTP/1.1 500") || !fio.closed[ref] {
			t.Errorf("%s: %q closed %v", c.name, fio.out[ref], fio.closed[ref])
		}
	}
	//: no stream port in the handler at all.
	app := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, r *corenet.HTTP1ResponseValue) { r.Stream = true })
	a := http1.HandleLoop(app, http1.Parser{})
	fio := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	oneReq(t, a, fio, ref, streamReq)
	if !strings.HasPrefix(string(fio.out[ref]), "HTTP/1.1 500") {
		t.Fatalf("%q", fio.out[ref])
	}
}

type streamingFunc struct{ corenet.HTTP1HandlerFunc }

func (streamingFunc) OnStreamOpen(corenet.HTTP1StreamValue)         {}
func (streamingFunc) OnStreamTimeout(corenet.HTTP1StreamValue)      {}
func (streamingFunc) OnStreamDrain(corenet.HTTP1StreamValue)        {}
func (streamingFunc) OnStreamClose(corenet.HTTP1StreamValue, error) {}

func TestStream_timeoutDrainAndCloseReachTheHandler(t *testing.T) {
	h := &streamH{}
	a := http1.HandleLoop(h, http1.Parser{})
	fio := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	oneReq(t, a, fio, ref, streamReq)
	a.OnTimeout(fio, ref)
	if len(h.timeout) != 1 || fio.closed[ref] {
		t.Fatalf("timeout: %v, closed %v", h.timeout, fio.closed[ref])
	}
	a.OnDrain(fio, ref)
	if len(h.drained) != 1 {
		t.Fatalf("drained: %v", h.drained)
	}
	a.OnClose(fio, ref, corenet.ServerClosed)
	if len(h.closed) != 1 || h.closed[0] != corenet.ServerClosed {
		t.Fatalf("closed: %v", h.closed)
	}
	//: a stale close says nothing more.
	a.OnClose(fio, ref, nil)
	if len(h.closed) != 1 {
		t.Fatalf("told twice: %v", h.closed)
	}
}

func TestStream_notStreamingLeftToTheDrain(t *testing.T) {
	h := &streamH{}
	a := http1.HandleLoop(h, http1.Parser{})
	fio := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	a.OnOpen(fio, ref)
	a.OnDrain(fio, ref)
	a.OnTimeout(fio, ref)
	if len(h.drained) != 0 || !fio.closed[ref] {
		t.Fatalf("drained %v closed %v", h.drained, fio.closed[ref])
	}
}

// TestStream_behindAChain holds that the adapter finds the stream port through
// the links, and that a stream on a link's answer keeps the links' fields.
func TestStream_behindAChain(t *testing.T) {
	h := &streamH{}
	chain := corenet.Chain[corenet.HTTP1Handler](h, http1.RequestID())
	a := http1.HandleLoop(chain, http1.Parser{})
	fio := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	oneReq(t, a, fio, ref, "GET / HTTP/1.1\r\nHost: h\r\nX-Request-Id: s-1\r\n\r\n")
	if len(h.opened) != 1 {
		t.Fatalf("opened %v", h.opened)
	}
	if !strings.Contains(string(fio.out[ref]), "X-Request-Id: s-1\r\n") {
		t.Fatalf("%q", fio.out[ref])
	}
}
