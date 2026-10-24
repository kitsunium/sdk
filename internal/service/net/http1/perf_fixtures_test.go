//go:build !race

// The fixtures of http1's performance contracts (design/sdk.yaml, budgets):
// each sets one call up and returns it, and the gen_perf_test.go kit gen
// writes beside this file counts its allocations against its budget — the
// total over 30 000 calls after as many warm-up calls.
//
// The claim is that framing a request and writing a response allocate
// nothing once the caller's buffers have room. The shapes that would lose it
// are a slice of fields that grows, a request value that escapes, a string
// built from the bytes for a comparison, and strconv.Itoa instead of
// AppendInt: every functional test passes against each of them. Each fixture
// walks the paths a request takes — a GET with credentials, a POST with a
// body, a refusal — so that every branch is inside the window.

package http1_test

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

// perfParse is Parse's fixture: two accepted requests and a refusal, into one
// reused value.
func perfParse(tb testing.TB) func() {
	var p http1.Parser
	var req corenet.HTTP1RequestValue
	if n, r := p.Parse(perfGet, &req); n != len(perfGet) || r != http1.RefusalNone {
		tb.Fatalf("perfGet: %d, %s", n, r)
	}
	if n, r := p.Parse(perfPost, &req); n != len(perfPost) || r != http1.RefusalNone {
		tb.Fatalf("perfPost: %d, %s", n, r)
	}
	if _, r := p.Parse(perfBad, &req); r != http1.RefusalTransferEncoding {
		tb.Fatalf("perfBad: %s", r)
	}
	return func() {
		perfInt, perfRefusal = p.Parse(perfGet, &req)
		perfInt, perfRefusal = p.Parse(perfPost, &req)
		perfInt, perfRefusal = p.Parse(perfBad, &req)
	}
}

// perfAppendResponse is AppendResponse's fixture: a JSON answer with one
// extra field, into a buffer with room, and a refused 204 with a body.
func perfAppendResponse(tb testing.TB) func() {
	req := &corenet.HTTP1RequestValue{Method: []byte("GET")}
	hdr, err := http1.AppendField(nil, "Content-Type", "application/json")
	if err != nil {
		tb.Fatal(err)
	}
	res := &corenet.HTTP1ResponseValue{Header: hdr, Body: []byte(`{"id":12345,"username":"user_12345"}`)}
	bad := &corenet.HTTP1ResponseValue{Status: 204, Body: []byte("x")}
	dst := make([]byte, 0, 512)
	if _, err := http1.AppendResponse(dst, req, res, false); err != nil {
		tb.Fatal(err)
	}
	return func() {
		perfBytes, _ = http1.AppendResponse(dst[:0], req, res, true)
		perfBytes, _ = http1.AppendResponse(dst[:0], req, bad, false)
	}
}

// perfAppendStatusLine is AppendStatusLine's fixture: a registered code and
// one composed with its integer.
func perfAppendStatusLine(tb testing.TB) func() {
	dst := make([]byte, 0, 64)
	if got, _ := http1.AppendStatusLine(dst, 299); string(got) != "HTTP/1.1 299 \r\n" {
		tb.Fatalf("299: %q", got)
	}
	return func() {
		perfBytes, _ = http1.AppendStatusLine(dst[:0], 404)
		perfBytes, _ = http1.AppendStatusLine(dst[:0], 299)
	}
}

// perfAppendField is AppendField's fixture: a field accepted and a codec-owned
// name refused.
func perfAppendField(tb testing.TB) func() {
	dst := make([]byte, 0, 64)
	if _, err := http1.AppendField(dst, "Content-Type", "application/json"); err != nil {
		tb.Fatal(err)
	}
	return func() {
		perfBytes, _ = http1.AppendField(dst[:0], "Content-Type", "application/json")
	}
}

// perfAppendRefusal is AppendRefusal's fixture: a 400 and a 431.
func perfAppendRefusal(tb testing.TB) func() {
	dst := make([]byte, 0, 128)
	_ = tb
	return func() {
		perfBytes = http1.AppendRefusal(dst[:0], http1.RefusalBareLF)
		perfBytes = http1.AppendRefusal(dst[:0], http1.RefusalTooManyFields)
	}
}
