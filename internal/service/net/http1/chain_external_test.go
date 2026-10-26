package http1_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// tokenVerifier knows a token per subject, and sets the claims to the
// subject's length.
type tokenVerifier map[string]string

func (v tokenVerifier) VerifyBearer(token []byte, auth *corenet.HTTP1AuthValue) bool {
	sub, ok := v[string(token)]
	if !ok {
		return false
	}
	auth.SetClaims(uint64(len(sub)))
	return auth.SetSubject([]byte(sub))
}

// recordH records what the chain left on each request it reached.
type recordH struct {
	routes   []int
	subjects []string
	headers  []string
	body     []byte
}

func (h *recordH) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	h.routes = append(h.routes, req.Route)
	sub := ""
	if req.Authenticated {
		sub = string(req.Auth.Subject())
	}
	h.subjects = append(h.subjects, sub)
	h.headers = append(h.headers, string(res.Header))
	res.Body = h.body
}

// parse frames in into a fresh request value.
func parse(t *testing.T, in string) *corenet.HTTP1RequestValue {
	t.Helper()
	var req corenet.HTTP1RequestValue
	if n, r := (http1.Parser{}).Parse([]byte(in), &req); n != len(in) || r != http1.RefusalNone {
		t.Fatalf("%q: %d, %s", in, n, r)
	}
	return &req
}

// mustLink returns the middleware, and panics on its error, which fails the
// test that built it.
func mustLink(mw corenet.Middleware[corenet.HTTP1Handler], err error) corenet.Middleware[corenet.HTTP1Handler] {
	if err != nil {
		panic(err)
	}
	return mw
}

// misconfiguredAs reports whether err is HTTP1Misconfigured naming option.
func misconfiguredAs(err error, option string) bool {
	return errs.HasCode(err, corenet.CodeHTTP1Misconfigured) && slices.ContainsFunc(errs.FieldsOf(err), func(f errs.FieldValue) bool {
		return f.Key() == "option" && f.StringValue() == option
	})
}

// TestRoutes pins the router link: the first route whose method and path
// match sets Route, a prefix matches a longer path, zero is "none", and the
// next handler runs either way.
func TestRoutes(t *testing.T) {
	t.Parallel()
	h := &recordH{}
	routes := mustLink(http1.Routes(
		http1.RouteValue{ID: 1, Method: "GET", Path: "/items/", Prefix: true},
		http1.RouteValue{ID: 2, Path: "/health"},
		http1.RouteValue{ID: 3, Method: "POST", Path: "/items/new"},
	))
	chain := corenet.Chain[corenet.HTTP1Handler](h, routes)
	for _, c := range []struct {
		in   string
		want int
	}{
		{get("/items/42"), 1},
		{get("/items/"), 1},
		{get("/items"), 0},
		{get("/health"), 2},
		{"HEAD /health HTTP/1.1\r\nHost: h\r\n\r\n", 2},
		{get("/health/x"), 0},
		{"POST /items/new HTTP/1.1\r\nHost: h\r\n\r\n", 3},
		{get("/nowhere"), 0},
		{"GET http://h/health?q HTTP/1.1\r\nHost: h\r\n\r\n", 2},
	} {
		var res corenet.HTTP1ResponseValue
		chain.ServeHTTP1(parse(t, c.in), &res)
		if got := h.routes[len(h.routes)-1]; got != c.want {
			t.Errorf("%q: Route %d, want %d", c.in, got, c.want)
		}
	}
	for _, c := range []struct {
		name, opt string
		routes    []http1.RouteValue
	}{
		{"zero ID", "id", []http1.RouteValue{{Path: "/"}}},
		{"repeated ID", "id", []http1.RouteValue{{ID: 1, Path: "/a"}, {ID: 1, Path: "/b"}}},
		{"no slash", "path", []http1.RouteValue{{ID: 1, Path: "a"}}},
		{"dot-segment", "path", []http1.RouteValue{{ID: 1, Path: "/a/../b"}}},
		{"empty segment", "path", []http1.RouteValue{{ID: 1, Path: "//a"}}},
		{"bad method", "method", []http1.RouteValue{{ID: 1, Method: "G T", Path: "/"}}},
	} {
		if _, err := http1.Routes(c.routes...); !misconfiguredAs(err, c.opt) {
			t.Errorf("%s: %v, want Misconfigured option=%s", c.name, err, c.opt)
		}
	}
}

// TestBearer pins the authenticating link: no token, another scheme or a
// token the verifier refuses is answered 401 with WWW-Authenticate and goes
// no further; an accepted one reaches the handler with the subject copied by
// value, so overwriting the token's bytes changes nothing; an exempt route
// goes on unauthenticated, whatever it carries.
func TestBearer(t *testing.T) {
	t.Parallel()
	h := &recordH{}
	v := tokenVerifier{"tok-a": "alice", "tok-b": "bob"}
	routes := mustLink(http1.Routes(http1.RouteValue{ID: 1, Path: "/api", Prefix: true}, http1.RouteValue{ID: 2, Path: "/health"}))
	bearer := mustLink(http1.Bearer(v, 2))
	chain := corenet.Chain[corenet.HTTP1Handler](h, routes, bearer)
	auth := func(target, value string) string {
		return "GET " + target + " HTTP/1.1\r\nHost: h\r\nAuthorization: " + value + "\r\n\r\n"
	}
	for _, c := range []struct {
		in, sub string
		status  int
	}{
		{get("/api/x"), "", 401},
		{auth("/api/x", "Basic dG9rLWE="), "", 401},
		{auth("/api/x", "Bearer"), "", 401},
		{auth("/api/x", "Bearer nope"), "", 401},
		{auth("/api/x", "Bearertok-a"), "", 401},
		{auth("/api/x", "Bearer tok-a"), "alice", 0},
		{auth("/api/x", "bEaReR    tok-b"), "bob", 0},
		{auth("/nowhere", "Bearer tok-b"), "bob", 0},
		{get("/nowhere"), "", 401},
		{get("/health"), "", 0},
		{auth("/health", "Bearer tok-a"), "", 0},
	} {
		served := len(h.subjects)
		var res corenet.HTTP1ResponseValue
		chain.ServeHTTP1(parse(t, c.in), &res)
		if res.Status != c.status {
			t.Errorf("%q: status %d, want %d", c.in, res.Status, c.status)
		}
		if c.status == 401 {
			if len(h.subjects) != served || string(res.Header) != "WWW-Authenticate: Bearer\r\n" {
				t.Errorf("%q: reached the handler, or header %q", c.in, res.Header)
			}
			continue
		}
		if got := h.subjects[len(h.subjects)-1]; got != c.sub {
			t.Errorf("%q: subject %q, want %q", c.in, got, c.sub)
		}
	}
	//: the subject is a copy: the token's bytes, reused, leave it as it was.
	in := []byte(auth("/api/x", "Bearer tok-a"))
	var req corenet.HTTP1RequestValue
	if n, r := (http1.Parser{}).Parse(in, &req); n != len(in) || r != http1.RefusalNone {
		t.Fatal(n, r)
	}
	var res corenet.HTTP1ResponseValue
	chain.ServeHTTP1(&req, &res)
	for i := range in {
		in[i] = 0xAA
	}
	if string(req.Auth.Subject()) != "alice" || req.Auth.Claims() != 5 || !req.Authenticated {
		t.Fatalf("Auth after the buffer was reused: %q %d %v", req.Auth.Subject(), req.Auth.Claims(), req.Authenticated)
	}
	//: a value left by an earlier link, or a stale request, never authenticates.
	req2 := parse(t, get("/api/x"))
	req2.Authenticated = true
	req2.Auth.SetSubject([]byte("mallory"))
	chain.ServeHTTP1(req2, &corenet.HTTP1ResponseValue{})
	if req2.Authenticated || len(req2.Auth.Subject()) != 0 {
		t.Fatal("a pre-set Auth survived the link")
	}
	if _, err := http1.Bearer(nil); !misconfiguredAs(err, "verifier") {
		t.Errorf("nil verifier: %v", err)
	}
	if _, err := http1.Bearer(v, 0); !misconfiguredAs(err, "exempt") {
		t.Errorf("exempt 0: %v", err)
	}
}

// TestHeaders pins the headers link: every answer carries its fields, a
// handler that appends copies them rather than write into the link's bytes,
// a 401 behind it keeps them, and the names the codec writes are refused.
func TestHeaders(t *testing.T) {
	t.Parallel()
	hdr := mustLink(http1.Headers("X-Frame-Options", "DENY", "Cache-Control", "no-store"))
	const fields = "X-Frame-Options: DENY\r\nCache-Control: no-store\r\n"
	appendH := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
		res.Header, _ = http1.AppendField(res.Header, "Content-Type", "text/plain")
	})
	chain := corenet.Chain[corenet.HTTP1Handler](appendH, hdr)
	for range 2 {
		var res corenet.HTTP1ResponseValue
		chain.ServeHTTP1(parse(t, get("/")), &res)
		if string(res.Header) != fields+"Content-Type: text/plain\r\n" {
			t.Fatalf("Header = %q", res.Header)
		}
	}
	v := tokenVerifier{}
	bearer := mustLink(http1.Bearer(v))
	refused := corenet.Chain[corenet.HTTP1Handler](&recordH{}, hdr, bearer)
	var res corenet.HTTP1ResponseValue
	refused.ServeHTTP1(parse(t, get("/")), &res)
	if res.Status != 401 || string(res.Header) != fields+"WWW-Authenticate: Bearer\r\n" {
		t.Fatalf("401 behind headers: %d %q", res.Status, res.Header)
	}
	//: the link's own bytes were never written into.
	chain.ServeHTTP1(parse(t, get("/")), &res)
	var plain corenet.HTTP1ResponseValue
	corenet.Chain[corenet.HTTP1Handler](&recordH{}, hdr).ServeHTTP1(parse(t, get("/")), &plain)
	if string(plain.Header) != fields {
		t.Fatalf("the link's fields became %q", plain.Header)
	}
	for _, c := range []struct {
		opt   string
		pairs []string
	}{
		{"pairs", []string{"X-A"}},
		{"Content-Length", []string{"Content-Length", "1"}},
		{"Connection", []string{"connection", "close"}},
		{"X A", []string{"X A", "v"}},
	} {
		if _, err := http1.Headers(c.pairs...); !misconfiguredAs(err, c.opt) && !(c.opt == "Connection" && misconfiguredAs(err, "connection")) {
			t.Errorf("%v: %v", c.pairs, err)
		}
	}
}

// accessRecorder keeps copies of the records it was handed.
type accessRecorder struct {
	recs []corenet.HTTP1AccessValue
	text []string
}

func (a *accessRecorder) LogHTTP1(rec corenet.HTTP1AccessValue) {
	a.recs = append(a.recs, rec)
	var b strings.Builder
	for i := range rec.NFields {
		b.WriteString(string(rec.Fields[i].Name) + "=" + string(rec.Fields[i].Value) + ";")
	}
	a.text = append(a.text, string(rec.Method)+" "+string(rec.Path)+" "+b.String())
}

// TestLog pins the access-log link: one record per request after the answer,
// the allowed fields only, never the query nor a credential, 200 for a zero
// status and none for a deferred answer; a credential, a repeat, a name that
// is not a token and too many names are refused.
func TestLog(t *testing.T) {
	t.Parallel()
	rec := &accessRecorder{}
	logLink := mustLink(http1.Log(rec, "User-Agent", "x-request-id"))
	deferH := corenet.HTTP1HandlerFunc(func(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
		res.Defer = bytes.Equal(req.Path, []byte("/later"))
		res.Body = []byte("abc")
	})
	chain := corenet.Chain[corenet.HTTP1Handler](deferH, logLink)
	in := "GET /now?secret=1 HTTP/1.1\r\nHost: h\r\nAuthorization: Bearer s3cret\r\nX-Request-ID: r1\r\nUser-Agent: ua\r\nCookie: c=1\r\n\r\n"
	chain.ServeHTTP1(parse(t, in), &corenet.HTTP1ResponseValue{})
	chain.ServeHTTP1(parse(t, get("/later")), &corenet.HTTP1ResponseValue{})
	if len(rec.recs) != 2 {
		t.Fatalf("%d records", len(rec.recs))
	}
	if r := rec.recs[0]; r.Status != 200 || r.BodyLen != 3 || r.Deferred || rec.text[0] != "GET /now User-Agent=ua;x-request-id=r1;" {
		t.Fatalf("record %+v %q", r, rec.text[0])
	}
	if r := rec.recs[1]; r.Status != 0 || !r.Deferred {
		t.Fatalf("deferred record %+v", r)
	}
	for _, s := range rec.text {
		if strings.Contains(s, "s3cret") || strings.Contains(s, "secret") || strings.Contains(s, "c=1") {
			t.Fatalf("a credential or the query was logged: %q", s)
		}
	}
	for _, c := range []struct {
		opt   string
		allow []string
	}{
		{"Authorization", []string{"Authorization"}},
		{"PROXY-AUTHORIZATION", []string{"PROXY-AUTHORIZATION"}},
		{"cookie", []string{"cookie"}},
		{"x-a", []string{"X-A", "x-a"}},
		{"", []string{""}},
		{"a b", []string{"a b"}},
		{"allow", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}},
	} {
		if _, err := http1.Log(rec, c.allow...); !misconfiguredAs(err, c.opt) {
			t.Errorf("%v: %v, want option=%q", c.allow, err, c.opt)
		}
	}
	if _, err := http1.Log(nil); !misconfiguredAs(err, "logger") {
		t.Errorf("nil logger: %v", err)
	}
}

// consumedLink writes the framing fields a link may not change: the adapter
// must frame with what Parse returned.
type consumedLink struct{ next corenet.HTTP1Handler }

func (l consumedLink) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	req.Consumed, req.HeadLen, req.Body = 1, 1, nil
	l.next.ServeHTTP1(req, res)
}

// EndBatch forwards, as every link of a chain that defers must.
func (l consumedLink) EndBatch(lane int) { l.next.(corenet.HTTP1Batcher).EndBatch(lane) }

// TestChain_onTheLoop pins the chain behind the loop adapter: deferred
// answers still complete through every layer's EndBatch, a link that writes
// Consumed frames nothing differently, OnStart builds the lanes and each
// request carries the lane that framed it.
func TestChain_onTheLoop(t *testing.T) {
	t.Parallel()
	h := &batchH{}
	v := tokenVerifier{"t": "u"}
	routes := mustLink(http1.Routes(http1.RouteValue{ID: 1, Path: "/", Prefix: true}))
	bearer := mustLink(http1.Bearer(v))
	hdr := mustLink(http1.Headers("X-A", "1"))
	logLink := mustLink(http1.Log(&accessRecorder{}))
	chain := corenet.Chain[corenet.HTTP1Handler](h, routes, bearer, hdr, logLink,
		func(next corenet.HTTP1Handler) corenet.HTTP1Handler { return consumedLink{next} })
	a := http1.HandleLoop(chain, http1.Parser{})
	a.OnStart(3)
	fio := newFakeIO(2)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	a.OnOpen(fio, ref)
	req := func(target string) string {
		return "GET " + target + " HTTP/1.1\r\nHost: h\r\nAuthorization: Bearer t\r\n\r\n"
	}
	in := req("/a") + req("/defer1") + req("/b")
	if n := a.OnData(fio, ref, []byte(in)); n != len(in) {
		t.Fatalf("OnData = %d, want %d", n, len(in))
	}
	a.OnBatchEnd(fio)
	got := answers(t, fio.out[ref], "GET")
	want := []string{"200 OK now:/a", "201 Created later:/defer1", "200 OK now:/b"}
	if !slices.Equal(got, want) {
		t.Fatalf("answers %q, want %q", got, want)
	}
	if !slices.Equal(h.loops, []int{2, 2, 2}) || !slices.Equal(h.ends, []int{2}) {
		t.Fatalf("lanes %v, ends %v", h.loops, h.ends)
	}
}
