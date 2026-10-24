package http1_test

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// fakeIO is a LoopIO with no engine behind it. With out set it records what
// each connection was sent and whether it was closed; without, Write only
// counts, so the allocation fixtures measure the adapter alone.
type fakeIO struct {
	// loop is what Loop answers.
	loop int
	// out records the bytes written per connection, when not nil.
	out map[corenet.LoopConnRef][]byte
	// closed records the connections closed, when not nil.
	closed map[corenet.LoopConnRef]bool
	// pressure is what Pressure answers.
	pressure bool
	// full refuses every Write with LoopOutputFull.
	full bool
	// writes counts the Write calls.
	writes int
}

func newFakeIO(loop int) *fakeIO {
	return &fakeIO{loop: loop, out: map[corenet.LoopConnRef][]byte{}, closed: map[corenet.LoopConnRef]bool{}}
}

func (f *fakeIO) Write(ref corenet.LoopConnRef, p []byte) error {
	f.writes++
	if f.closed[ref] {
		return corenet.LoopStaleRef
	}
	if f.full {
		return corenet.LoopOutputFull
	}
	if f.out != nil {
		f.out[ref] = append(f.out[ref], p...)
	}
	return nil
}

func (f *fakeIO) Close(ref corenet.LoopConnRef) error {
	if f.closed == nil {
		return nil
	}
	if f.closed[ref] {
		return corenet.LoopStaleRef
	}
	f.closed[ref] = true
	return nil
}

func (*fakeIO) SetDeadline(corenet.LoopConnRef, time.Duration) error { return nil }
func (f *fakeIO) Pressure(corenet.LoopConnRef) bool                  { return f.pressure }
func (*fakeIO) Post(corenet.PostFunc, uintptr) error                 { return nil }
func (f *fakeIO) Loop() int                                          { return f.loop }

// batchH serves by target: "/defer…" is deferred and answered at EndBatch,
// "/bad" asks for a status the writer refuses, "/close" closes, and anything
// else is answered at once with its target.
type batchH struct {
	// served are the targets served, in order.
	served []string
	// loops are the Loop of each request served.
	loops []int
	// ends are the loops EndBatch was called for.
	ends []int
	// pending are the deferred responses of the batch, with their target.
	pending []*corenet.HTTP1ResponseValue
	targets []string
	// leave leaves the deferred responses deferred.
	leave bool
}

func (h *batchH) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	target := string(req.Target)
	h.served = append(h.served, target)
	h.loops = append(h.loops, req.Loop)
	switch {
	case strings.HasPrefix(target, "/defer"):
		res.Defer = true
		h.pending = append(h.pending, res)
		h.targets = append(h.targets, target)
	case target == "/bad":
		res.Status = 100
	case target == "/close":
		res.Close = true
		res.Body = []byte("bye")
	default:
		res.Body = []byte("now:" + target)
	}
}

func (h *batchH) EndBatch(loop int) {
	h.ends = append(h.ends, loop)
	for i, res := range h.pending {
		if h.leave {
			continue
		}
		res.Status = 201
		res.Body = []byte("later:" + h.targets[i])
		res.Defer = false
	}
	h.pending, h.targets = h.pending[:0], h.targets[:0]
}

// get spells a GET of target, keep-alive.
func get(target string) string {
	return "GET " + target + " HTTP/1.1\r\nHost: h\r\n\r\n"
}

// answers reads every response in b, each to a request of method.
func answers(t *testing.T, b []byte, method string) []string {
	t.Helper()
	r := bufio.NewReader(bytes.NewReader(b))
	var out []string
	for {
		if _, err := r.Peek(1); err != nil {
			return out
		}
		res, err := http.ReadResponse(r, &http.Request{Method: method})
		if err != nil {
			t.Fatalf("ReadResponse after %q: %v", out, err)
		}
		body, _ := io.ReadAll(res.Body)
		line := res.Status + " " + string(body)
		if res.Close {
			line += " [close]"
		}
		out = append(out, line)
	}
}

// TestHandleLoop_order pins the batch: immediate and deferred answers on one
// connection leave in request order, EndBatch runs once with the loop's
// index, the requests carry it, and a partial request waits in the spill.
func TestHandleLoop_order(t *testing.T) {
	t.Parallel()
	h := &batchH{}
	a := http1.HandleLoop(h, http1.Parser{})
	fio := newFakeIO(3)
	ref := corenet.LoopConnRef{Slot: 7, Gen: 1}
	a.OnOpen(fio, ref)
	in := get("/a") + get("/defer1") + get("/b") + get("/defer2") + "GET /c HTTP/1.1\r\nHo"
	if n := a.OnData(fio, ref, []byte(in)); n != len(in)-len("GET /c HTTP/1.1\r\nHo") {
		t.Fatalf("OnData = %d, want the four whole requests", n)
	}
	if len(fio.out[ref]) != 0 {
		t.Fatalf("written before the batch's end: %q", fio.out[ref])
	}
	a.OnBatchEnd(fio)
	got := answers(t, fio.out[ref], http.MethodGet)
	want := []string{"200 OK now:/a", "201 Created later:/defer1", "200 OK now:/b", "201 Created later:/defer2"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("answers = %q, want %q", got, want)
	}
	if len(h.ends) != 1 || h.ends[0] != 3 || fio.closed[ref] {
		t.Fatalf("EndBatch loops %v, closed %v", h.ends, fio.closed[ref])
	}
	for _, l := range h.loops {
		if l != 3 {
			t.Fatalf("Request.Loop = %v, want 3", h.loops)
		}
	}
	// A batch that served nothing does not end the handler's batch.
	a.OnBatchEnd(fio)
	if len(h.ends) != 1 {
		t.Fatalf("EndBatch on an empty batch: %v", h.ends)
	}
}

// TestHandleLoop_closes pins every way a connection ends: the request's,
// the response's or the pressure's Connection: close, a refusal, a status
// the writer refuses, an answer left deferred, and a refused Write. Nothing
// past a request known to close reaches the handler; requests pipelined
// behind an answer that only closes at the batch's end were served, and their
// answers are dropped with the connection.
func TestHandleLoop_closes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		in       string
		leave    bool
		pressure bool
		full     bool
		want     []string
		served   int
	}
	tests := []tc{
		{name: "the request asks", in: "GET /a HTTP/1.1\r\nConnection: close\r\n\r\n" + get("/b"), want: []string{"200 OK now:/a [close]"}, served: 1},
		{name: "the response asks", in: get("/close") + get("/b"), want: []string{"200 OK bye [close]"}, served: 1},
		{name: "the pressure asks", in: get("/a") + get("/b"), pressure: true, want: []string{"200 OK now:/a [close]"}, served: 1},
		{name: "a refusal", in: get("/a") + "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n" + get("/b"), want: []string{"200 OK now:/a", "400 Bad Request  [close]"}, served: 1},
		{name: "a status the writer refuses", in: get("/bad") + get("/b"), want: []string{"500 Internal Server Error  [close]"}, served: 1},
		{name: "an answer left deferred", in: get("/defer") + get("/b"), leave: true, want: []string{"500 Internal Server Error  [close]"}, served: 3},
		{name: "the output refuses", in: get("/a"), full: true, served: 2},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := &batchH{leave: c.leave}
			a := http1.HandleLoop(h, http1.Parser{})
			fio := newFakeIO(0)
			fio.pressure, fio.full = c.pressure, c.full
			ref := corenet.LoopConnRef{Slot: 1, Gen: 4}
			a.OnOpen(fio, ref)
			if n := a.OnData(fio, ref, []byte(c.in)); n != len(c.in) {
				t.Fatalf("OnData = %d, want %d", n, len(c.in))
			}
			// More bytes for a connection that is closing are dropped unread.
			if n := a.OnData(fio, ref, []byte(get("/late"))); n != len(get("/late")) {
				t.Fatalf("OnData after the close = %d", n)
			}
			a.OnBatchEnd(fio)
			got := answers(t, fio.out[ref], http.MethodGet)
			if strings.Join(got, "|") != strings.Join(c.want, "|") || len(h.served) != c.served {
				t.Fatalf("answers %q served %q; want %q and %d served", got, h.served, c.want, c.served)
			}
			if !fio.closed[ref] {
				t.Fatalf("the connection stayed open")
			}
		})
	}
}

// TestHandleLoop_headAndConnections pins a deferred HEAD (its Content-Length
// and no body), two connections interleaved in one batch, a slot reused by a
// newer connection, a timeout, and a handler that is not a batcher.
func TestHandleLoop_headAndConnections(t *testing.T) {
	t.Parallel()
	h := &batchH{}
	a := http1.HandleLoop(h, http1.Parser{})
	fio := newFakeIO(0)
	r1, r2 := corenet.LoopConnRef{Slot: 1, Gen: 1}, corenet.LoopConnRef{Ring: 2, Slot: 900, Gen: 1}
	a.OnOpen(fio, r1)
	a.OnOpen(fio, r2)
	a.OnData(fio, r1, []byte("HEAD /defer HTTP/1.1\r\n\r\n"))
	a.OnData(fio, r2, []byte(get("/x")))
	a.OnData(fio, r1, []byte(get("/y")))
	a.OnBatchEnd(fio)
	if got := fio.out[r1]; !bytes.HasPrefix(got, []byte("HTTP/1.1 201 Created\r\nContent-Length: 12\r\n\r\nHTTP/1.1 200")) {
		t.Fatalf("r1 = %q", got)
	}
	if got := answers(t, fio.out[r2], http.MethodGet); len(got) != 1 || got[0] != "200 OK now:/x" {
		t.Fatalf("r2 = %q", got)
	}
	// The slot reused by a newer connection starts clean.
	a.OnData(fio, r1, []byte("GET / HTTP/1.1\r\nConnection: close\r\n\r\n"))
	a.OnBatchEnd(fio)
	a.OnClose(fio, r1, nil)
	r1b := corenet.LoopConnRef{Slot: 1, Gen: 2}
	a.OnOpen(fio, r1b)
	a.OnData(fio, r1b, []byte(get("/z")))
	a.OnBatchEnd(fio)
	if got := answers(t, fio.out[r1b], http.MethodGet); len(got) != 1 || got[0] != "200 OK now:/z" {
		t.Fatalf("reused slot = %q", got)
	}
	a.OnTimeout(fio, r2)
	a.OnWritable(fio, r2)
	if !fio.closed[r2] {
		t.Fatalf("a timeout the adapter never armed left the connection open")
	}
	// A plain handler is served without a batch end.
	plain := http1.HandleLoop(corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
		res.Body = []byte("plain")
	}), http1.Parser{})
	f2 := newFakeIO(1)
	plain.OnOpen(f2, r1)
	plain.OnData(f2, r1, []byte(get("/")))
	plain.OnBatchEnd(f2)
	if got := answers(t, f2.out[r1], http.MethodGet); len(got) != 1 || got[0] != "200 OK plain" {
		t.Fatalf("plain = %q", got)
	}
}

// TestHandleLoop_loops pins the per-loop state: two loops serving at once,
// each through its own LoopIO, keep their requests and answers apart.
func TestHandleLoop_loops(t *testing.T) {
	t.Parallel()
	h := &batchH{}
	a := http1.HandleLoop(h, http1.Parser{})
	f0, f5 := newFakeIO(0), newFakeIO(5)
	ref := corenet.LoopConnRef{Slot: 0, Gen: 1}
	a.OnOpen(f5, ref)
	a.OnOpen(f0, ref)
	a.OnData(f5, ref, []byte(get("/five")))
	a.OnData(f0, ref, []byte(get("/zero")))
	a.OnBatchEnd(f5)
	a.OnBatchEnd(f0)
	if got := answers(t, f5.out[ref], http.MethodGet); len(got) != 1 || got[0] != "200 OK now:/five" {
		t.Fatalf("loop 5 = %q", got)
	}
	if got := answers(t, f0.out[ref], http.MethodGet); len(got) != 1 || got[0] != "200 OK now:/zero" {
		t.Fatalf("loop 0 = %q", got)
	}
	if h.loops[0] != 5 || h.loops[1] != 0 || h.ends[0] != 5 || h.ends[1] != 0 {
		t.Fatalf("loops %v, ends %v", h.loops, h.ends)
	}
}
