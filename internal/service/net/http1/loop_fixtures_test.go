package http1_test

// The loop adapter's fixtures, shared by the allocation budgets of
// perf_fixtures_test.go (race off) and the benchmarks, which run under race
// as well: so this file carries no build tag.

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// perfGet and perfPost are the two requests of the challenge's API, as a load
// generator sends them; perfBad is refused at its Transfer-Encoding.
var (
	perfGet  = []byte("GET /users/12345 HTTP/1.1\r\nHost: 127.0.0.1:3000\r\nUser-Agent: k6/2.3.0 (https://k6.io/)\r\nAuthorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NSIsIm5hbWUiOiJ1c2VyIiwiZXhwIjo0MTAyNDQ0ODAwfQ.c2lnbmF0dXJlc2lnbmF0dXJlc2lnbmF0dXJlc2ln\r\nAccept-Encoding: gzip\r\n\r\n")
	perfPost = []byte("POST /users HTTP/1.1\r\nHost: 127.0.0.1:3000\r\nUser-Agent: k6/2.3.0 (https://k6.io/)\r\nContent-Type: application/json\r\nContent-Length: 44\r\nAuthorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln\r\n\r\n{\"username\":\"user_12345\",\"email\":\"u@e.test\"}")
	perfBad  = []byte("POST /users HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\n\r\n")
)

// perfH is the loop fixtures' handler: a request whose target starts with
// "/d" is deferred and completed at EndBatch, any other answered at once,
// every body the same preallocated slice — what a handler that owns its
// buffers does.
type perfH struct {
	// pending are the batch's deferred responses; its capacity is grown once.
	pending []*corenet.HTTP1ResponseValue
	// body is every answer's body.
	body []byte
}

func (h *perfH) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	if len(req.Target) > 1 && req.Target[1] == 'd' {
		res.Defer = true
		h.pending = append(h.pending, res)
		return
	}
	res.Body = h.body
}

func (h *perfH) EndBatch(int) {
	for _, res := range h.pending {
		res.Body, res.Defer = h.body, false
	}
	h.pending = h.pending[:0]
}

// perfLoopBatch is one batch of the loop fixtures: four pipelined requests on
// one connection — a GET, a deferred GET, a POST and a deferred GET — and a
// refused request on another, which closes it.
var perfLoopBatch = func() []byte {
	b := append([]byte(nil), perfGet...)
	b = append(b, "GET /deferred HTTP/1.1\r\nHost: h\r\n\r\n"...)
	b = append(b, perfPost...)
	return append(b, "GET /d2 HTTP/1.1\r\nHost: h\r\n\r\n"...)
}()

// perfLoop sets an adapter up on a counting LoopIO and returns one batch:
// both connections opened again, their bytes handed over, the batch ended.
func perfLoop(tb testing.TB) func() {
	h := &perfH{body: []byte(`{"id":12345,"username":"user_12345"}`)}
	a := http1.HandleLoop(h, http1.Parser{})
	io := &fakeIO{loop: 1}
	r1, r2 := corenet.LoopConnRef{Slot: 3, Gen: 1}, corenet.LoopConnRef{Slot: 4, Gen: 1}
	batch := func() {
		a.OnOpen(io, r1)
		a.OnOpen(io, r2)
		perfInt = a.OnData(io, r1, perfLoopBatch)
		perfInt += a.OnData(io, r2, perfBad)
		a.OnBatchEnd(io)
	}
	batch()
	if perfInt != len(perfLoopBatch)+len(perfBad) || io.writes != 2 {
		tb.Fatalf("one batch took %d bytes in %d writes", perfInt, io.writes)
	}
	return batch
}
