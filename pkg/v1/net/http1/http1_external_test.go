// Package http1_test — black-box tests for the public profile: a handler
// written against this package and server alone, served on a loop group, and
// the codec reached through the facade.
package http1_test

import (
	"bufio"
	"errors"
	"io"
	stdnet "net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/net/http1"
	"github.com/kitsunium/sdk/pkg/v1/net/server"
)

// perLoop is what the handler keeps for one loop: what a consumer keeps per
// loop — a verification scratch, a batch — needs no lock, since one thread
// calls it.
type perLoop struct {
	// pending are the batch's deferred answers.
	pending []*http1.Response
	// ids are their ids, in the same order.
	ids []int
	// batches counts the batches ended.
	batches int
	// body is the loop's answer buffer, reused every batch.
	body []byte
}

// counter answers GET /n/<id> at the end of the batch with "<id> <batch>",
// GET /now at once, and anything else 404. It is a Batcher.
type counter struct {
	// mu guards loops' growth: a loop's own entry is its thread's.
	mu sync.Mutex
	// loops holds each loop's state at its index.
	loops []*perLoop
}

// Compile-time proof that a consumer implements both ports in public.
var (
	_ http1.Handler = (*counter)(nil)
	_ http1.Batcher = (*counter)(nil)
)

func (c *counter) loop(i int) *perLoop {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.loops) <= i {
		c.loops = append(c.loops, &perLoop{})
	}
	return c.loops[i]
}

func (c *counter) ServeHTTP1(req *http1.Request, res *http1.Response) {
	l := c.loop(req.Lane)
	target := string(req.Target)
	switch {
	case strings.HasPrefix(target, "/n/"):
		id, err := strconv.Atoi(target[3:])
		if err != nil {
			res.Status = 400
			return
		}
		res.Defer = true
		l.pending = append(l.pending, res)
		l.ids = append(l.ids, id)
	case target == "/now":
		res.Body = []byte("now")
	default:
		res.Status = 404
	}
}

func (c *counter) EndBatch(loop int) {
	l := c.loop(loop)
	l.batches++
	l.body = l.body[:0]
	// One pass over the batch, as one transaction would answer it; the
	// bodies share the loop's buffer, valid until the batch's end.
	for i, res := range l.pending {
		start := len(l.body)
		l.body = strconv.AppendInt(l.body, int64(l.ids[i]), 10)
		l.body = append(l.body, ' ')
		l.body = strconv.AppendInt(l.body, int64(l.batches), 10)
		res.Body = l.body[start:len(l.body):len(l.body)]
		res.Defer = false
	}
	l.pending, l.ids = l.pending[:0], l.ids[:0]
}

// TestHandleLoopServesHTTP pins ADR 0186 §D9 step 12 end to end: a Batcher
// written against the public packages serves keep-alive and pipelined
// requests on a loop group, deferred and immediate answers in request order,
// and a refused request is answered and closed before the handler sees it.
func TestHandleLoopServesHTTP(t *testing.T) {
	backends := []server.LoopBackend{server.LoopBackendAuto, server.LoopBackendPortable}
	//: Linux has a second native backend to serve on.
	if runtime.GOOS == "linux" {
		backends = append(backends, server.LoopBackendEpoll)
	}
	for _, b := range backends {
		t.Run(b.String(), func(t *testing.T) { servesHTTP(t, b) })
	}
}

// servesHTTP is TestHandleLoopServesHTTP on backend b.
func servesHTTP(t *testing.T, b server.LoopBackend) {
	h := &counter{}
	srv := server.New()
	t.Cleanup(func() { _ = srv.Close() })
	srv.Group("api", server.Listen("tcp", "127.0.0.1:0"), server.IdleTimeout(time.Minute),
		server.Loop(server.LoopValue{Backend: b}), server.Plaintext()).HandleLoop(http1.HandleLoop(h, http1.Parser{}))
	//: served on the platform's best loop backend.
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	addr := srv.State().Listeners[0].Address
	// net/http's client, keep-alive: one connection, several requests.
	client := &http.Client{Timeout: 10 * time.Second}
	for i, c := range []struct {
		target, want string
		status       int
	}{
		{"/n/7", "7 ", 200}, {"/now", "now", 200}, {"/n/8", "8 ", 200}, {"/missing", "", 404},
	} {
		res, err := client.Get("http://" + addr + c.target)
		if err != nil {
			t.Fatalf("GET %s: %v", c.target, err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		//: the status, and the body's prefix (the batch number follows).
		if res.StatusCode != c.status || !strings.HasPrefix(string(body), c.want) {
			t.Fatalf("request %d %s = %d %q, want %d %q…", i, c.target, res.StatusCode, body, c.status, c.want)
		}
	}
	client.CloseIdleConnections()
	// Pipelined, deferred and immediate interleaved, in one write.
	conn, err := stdnet.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	var pipe strings.Builder
	for _, target := range []string{"/n/1", "/now", "/n/2", "/n/3", "/now"} {
		pipe.WriteString("GET " + target + " HTTP/1.1\r\nHost: h\r\n\r\n")
	}
	_, _ = conn.Write([]byte(pipe.String()))
	r := bufio.NewReader(conn)
	//: every answer, in request order.
	for _, want := range []string{"1 ", "now", "2 ", "3 ", "now"} {
		res, err := http.ReadResponse(r, &http.Request{Method: http.MethodGet})
		if err != nil {
			t.Fatalf("pipelined answer %q: %v", want, err)
		}
		body, _ := io.ReadAll(res.Body)
		if !strings.HasPrefix(string(body), want) {
			t.Fatalf("pipelined = %q, want %q…", body, want)
		}
	}
	// A refused request: answered 400 with Connection: close, then closed.
	_, _ = conn.Write([]byte("POST /n/9 HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n"))
	res, err := http.ReadResponse(r, &http.Request{Method: http.MethodPost})
	if err != nil || res.StatusCode != 400 || !res.Close {
		t.Fatalf("refusal = %v, %v", res, err)
	}
	//: the server closes after the refusal.
	if _, err := r.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("after the refusal: %v, want EOF", err)
	}
	//: the drain stops the loops before their state is read.
	if err := srv.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	batches := 0
	for _, l := range h.loops {
		batches += l.batches
	}
	//: every deferred answer was completed by an EndBatch.
	if batches == 0 {
		t.Fatalf("EndBatch never ran")
	}
}

// TestCodecThroughTheFacade pins the codec and the sentinels as a consumer
// reaches them: a request framed by a Parser, an answer written, a refusal
// branched on by its sentinel, and a Limits NewParser refuses.
func TestCodecThroughTheFacade(t *testing.T) {
	t.Parallel()
	var p http1.Parser
	var req http1.Request
	in := []byte("GET /x HTTP/1.1\r\nHost: h\r\nAuthorization: Bearer t\r\n\r\n")
	//: framed whole.
	if n, r := p.Parse(in, &req); n != len(in) || r != http1.RefusalNone || string(req.Authorization) != "Bearer t" {
		t.Fatalf("Parse = %d, %v, %q", n, r, req.Authorization)
	}
	hdr, err := http1.AppendField(nil, "Content-Type", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	out, err := http1.AppendResponse(nil, &req, &http1.Response{Header: hdr, Body: []byte("hi")}, false)
	//: the codec's framing around the handler's field and body.
	if err != nil || string(out) != "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 2\r\n\r\nhi" {
		t.Fatalf("AppendResponse = %q, %v", out, err)
	}
	_, r := p.Parse([]byte("GET / HTTP/1.1\r\nExpect: 100-continue\r\n\r\n"), &req)
	//: a refusal is branched on by its sentinel.
	if !errors.Is(r.Err(), http1.ExpectRefused) || r.Status() != 417 {
		t.Fatalf("Expect = %v (%d)", r.Err(), r.Status())
	}
	//: an impossible bound is refused when the parser is built.
	if _, err := http1.NewParser(http1.Limits{Head: http1.MaxHead + 1}); !errors.Is(err, http1.Misconfigured) {
		t.Fatalf("NewParser = %v, want Misconfigured", err)
	}
	//: a refusal's canned answer closes.
	if got := http1.AppendRefusal(nil, r); !strings.Contains(string(got), "Connection: close") {
		t.Fatalf("AppendRefusal = %q", got)
	}
}
