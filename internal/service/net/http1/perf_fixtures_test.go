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

// perfLoopHandlerOnData is OnData's fixture: a batch, since OnData's answers are
// only released by the batch's end.
func perfLoopHandlerOnData(tb testing.TB) func() { return perfLoop(tb) }

// perfLoopHandlerOnBatchEnd is OnBatchEnd's fixture: the same batch, deferred
// answers completed and written.
func perfLoopHandlerOnBatchEnd(tb testing.TB) func() { return perfLoop(tb) }

// perfChain is the chain's fixture: L2's links — an access log, a router, a
// Bearer check and the response's fields — over a handler that answers by route,
// fed an authenticated request, an unrouted one, an unauthenticated one (401)
// and one on an exempt route, every request framed into one reused value.
func perfChain(tb testing.TB) func() {
	routes, err := http1.Routes(
		http1.RouteValue{ID: 1, Method: "GET", Path: "/api/v1/items/", Prefix: true},
		http1.RouteValue{ID: 2, Method: "GET", Path: "/api/v1/users/", Prefix: true},
		http1.RouteValue{ID: 3, Path: "/health"},
	)
	if err != nil {
		tb.Fatal(err)
	}
	bearer, err := http1.Bearer(perfVerifier{}, 3)
	if err != nil {
		tb.Fatal(err)
	}
	headers, err := http1.Headers("Content-Type", "application/json")
	if err != nil {
		tb.Fatal(err)
	}
	logLink, err := http1.Log(perfLogger{}, "User-Agent")
	if err != nil {
		tb.Fatal(err)
	}
	body := []byte(`{"id":42}`)
	app := corenet.HTTP1HandlerFunc(func(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
		switch req.Route {
		case 1, 2:
			res.Body = body
		case 3:
			res.Status = 204
		default:
			res.Status = 404
		}
	})
	//: the 401 leaves before Headers: a 401 behind it copies its field after
	//: Headers' own, the one error path that allocates (design/sdk.yaml).
	chain := corenet.Chain[corenet.HTTP1Handler](app, logLink, routes, bearer, headers)
	var p http1.Parser
	var req corenet.HTTP1RequestValue
	var res corenet.HTTP1ResponseValue
	serve := func(in []byte) {
		if n, r := p.Parse(in, &req); n != len(in) || r != http1.RefusalNone {
			tb.Fatalf("%q: %d, %s", in, n, r)
		}
		res = corenet.HTTP1ResponseValue{}
		chain.ServeHTTP1(&req, &res)
	}
	serve(perfChainAuthed)
	if res.Status != 0 || string(res.Body) != string(body) || string(req.Auth.Subject()) != "user-42" {
		tb.Fatalf("authenticated: %d %q %q", res.Status, res.Body, req.Auth.Subject())
	}
	serve(perfChainAnon)
	if res.Status != 401 {
		tb.Fatalf("unauthenticated: %d", res.Status)
	}
	return func() {
		serve(perfChainAuthed)
		serve(perfChainUnrouted)
		serve(perfChainAnon)
		serve(perfChainHealth)
	}
}

// The chain fixture's requests.
var (
	perfChainAuthed   = []byte("GET /api/v1/items/42 HTTP/1.1\r\nHost: h\r\nUser-Agent: k6\r\nAuthorization: Bearer good\r\n\r\n")
	perfChainUnrouted = []byte("GET /nowhere HTTP/1.1\r\nHost: h\r\nAuthorization: Bearer good\r\n\r\n")
	perfChainAnon     = []byte("GET /api/v1/users/7 HTTP/1.1\r\nHost: h\r\n\r\n")
	perfChainHealth   = []byte("GET /health HTTP/1.1\r\nHost: h\r\n\r\n")
	perfGood          = []byte("good")
	perfSubject       = []byte("user-42")
)

// perfVerifier accepts the one token "good" for user-42.
type perfVerifier struct{}

func (perfVerifier) VerifyBearer(token []byte, auth *corenet.HTTP1AuthValue) bool {
	if string(token) != string(perfGood) {
		return false
	}
	auth.SetClaims(1)
	return auth.SetSubject(perfSubject)
}

// perfLogger drops every record, as a logger that queues it would, once.
type perfLogger struct{}

func (perfLogger) LogHTTP1(rec corenet.HTTP1AccessValue) { perfInt = rec.Status }

// perfRouteLayerServeHTTP1 is the router link's fixture: the chain.
func perfRouteLayerServeHTTP1(tb testing.TB) func() { return perfChain(tb) }

// perfBearerLayerServeHTTP1 is the authenticating link's fixture: the chain.
func perfBearerLayerServeHTTP1(tb testing.TB) func() { return perfChain(tb) }

// perfHeadersLayerServeHTTP1 is the headers link's fixture: the chain.
func perfHeadersLayerServeHTTP1(tb testing.TB) func() { return perfChain(tb) }

// perfLogLayerServeHTTP1 is the access-log link's fixture: the chain.
func perfLogLayerServeHTTP1(tb testing.TB) func() { return perfChain(tb) }
