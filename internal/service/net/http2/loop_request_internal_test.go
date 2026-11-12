package http2

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// mapFields fills a request value's Fields from name/value pairs, as Decode would.
func mapFields(req *corenet.HTTP1RequestValue, pairs ...string) {
	req.NFields = 0
	for i := 0; i+1 < len(pairs); i += 2 {
		req.Fields[req.NFields] = corenet.HTTP1FieldValue{Name: []byte(pairs[i]), Value: []byte(pairs[i+1])}
		req.NFields++
	}
}

// TestMapRequest pins #962's mapping of the pseudo-fields onto the value the
// chain reads, and every refusal a stream gets instead.
func TestMapRequest(t *testing.T) {
	t.Parallel()
	st := &laneState{}
	for _, tc := range []struct {
		name       string
		pairs      []string
		refusal    http1.Refusal
		malformed  bool
		path       string
		host       string
		wantFields int
	}{
		{name: "plain get", pairs: []string{":method", "GET", ":scheme", "http", ":path", "/a?q=1", ":authority", "h"}, path: "/a", host: "h"},
		{name: "host field", pairs: []string{":method", "GET", ":scheme", "http", ":path", "/a", "host", "h"}, path: "/a", host: "h", wantFields: 1},
		{name: "regular field after", pairs: []string{":method", "GET", ":scheme", "http", ":path", "/a", "x-a", "1"}, path: "/a", wantFields: 1},
		{name: "dot segment", pairs: []string{":method", "GET", ":scheme", "http", ":path", "/a/../b", ":authority", "h"}, refusal: http1.RefusalTargetPath},
		{name: "authority and host differ", pairs: []string{":method", "GET", ":scheme", "http", ":path", "/a", ":authority", "a", "host", "b"}, refusal: http1.RefusalTargetHost},
		{name: "absolute form", pairs: []string{":method", "GET", ":scheme", "http", ":path", "http://h/a"}, refusal: http1.RefusalTargetForm},
		{name: "connect", pairs: []string{":method", "CONNECT", ":authority", "h:443"}, refusal: http1.RefusalMethodUnsupported},
		{name: "expect", pairs: []string{":method", "GET", ":scheme", "http", ":path", "/a", "expect", "100-continue"}, refusal: http1.RefusalExpect},
		{name: "no method", pairs: []string{":scheme", "http", ":path", "/a"}, malformed: true},
		{name: "no path", pairs: []string{":method", "GET", ":scheme", "http"}, malformed: true},
		{name: "repeated method", pairs: []string{":method", "GET", ":method", "POST", ":scheme", "http", ":path", "/a"}, malformed: true},
		{name: "pseudo after regular", pairs: []string{":method", "GET", "x-a", "1", ":scheme", "http", ":path", "/a"}, malformed: true},
		{name: "unknown pseudo", pairs: []string{":method", "GET", ":scheme", "http", ":path", "/a", ":foo", "bar"}, malformed: true},
		{name: "te gzip", pairs: []string{":method", "GET", ":scheme", "http", ":path", "/a", "te", "gzip"}, malformed: true},
		{name: "connection", pairs: []string{":method", "GET", ":scheme", "http", ":path", "/a", "connection", "close"}, malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req corenet.HTTP1RequestValue
			mapFields(&req, tc.pairs...)
			v := mapRequest(&req, st, 7)
			if v.malformed != tc.malformed || v.refusal != tc.refusal {
				t.Fatalf("verdict malformed=%v refusal=%s, want %v %s", v.malformed, v.refusal, tc.malformed, tc.refusal)
			}
			if tc.malformed || tc.refusal != http1.RefusalNone {
				return
			}
			if req.Proto != 2 || req.Stream != 7 || string(req.Path) != tc.path || req.NFields != tc.wantFields {
				t.Fatalf("Proto %d Stream %d Path %q NFields %d", req.Proto, req.Stream, req.Path, req.NFields)
			}
			if tc.host != "" && (!req.HasHost || string(req.Host) != tc.host) {
				t.Fatalf("Host %q HasHost %v", req.Host, req.HasHost)
			}
		})
	}
}

// TestMapRequest_cookiesJoin pins §8.2.3: split Cookie fields read as one.
func TestMapRequest_cookiesJoin(t *testing.T) {
	t.Parallel()
	st := &laneState{}
	var req corenet.HTTP1RequestValue
	mapFields(&req, ":method", "GET", ":scheme", "http", ":path", "/a", "cookie", "a=1", "cookie", "b=2")
	if v := mapRequest(&req, st, 1); v.malformed || v.refusal != http1.RefusalNone {
		t.Fatalf("verdict %+v", v)
	}
	if got, _ := req.Field("cookie"); string(got) != "a=1; b=2" {
		t.Fatalf("cookie = %q", got)
	}
}

// chainVerifier accepts one token.
type chainVerifier struct{}

func (chainVerifier) VerifyBearer(token []byte, auth *corenet.HTTP1AuthValue) bool {
	return string(token) == "tok" && auth.SetSubject([]byte("user"))
}

// TestMapRequest_chainAsHTTP1 pins #962's third criterion: the P2 chain (Routes
// then Bearer, /health exempt) routes and authenticates a mapped HTTP/2 request
// exactly as the same request parsed from HTTP/1.1.
func TestMapRequest_chainAsHTTP1(t *testing.T) {
	t.Parallel()
	routes, err := http1.Routes(http1.RouteValue{ID: 1, Path: "/whoami"}, http1.RouteValue{ID: 2, Path: "/health"})
	if err != nil {
		t.Fatal(err)
	}
	bearer, err := http1.Bearer(chainVerifier{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		route, status int
		subject       string
	}
	var got outcome
	h := corenet.Chain[corenet.HTTP1Handler](corenet.HTTP1HandlerFunc(func(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
		got = outcome{route: req.Route, status: res.Status, subject: string(req.Auth.Subject())}
	}), routes, bearer)
	st := &laneState{}
	run := func(req *corenet.HTTP1RequestValue) outcome {
		got = outcome{}
		res := corenet.HTTP1ResponseValue{}
		h.ServeHTTP1(req, &res)
		if got == (outcome{}) {
			return outcome{status: res.Status}
		}
		return got
	}
	for _, tc := range []struct{ path, auth string }{
		{"/whoami", "Bearer tok"}, {"/whoami", "Bearer bad"}, {"/whoami", ""}, {"/health", ""}, {"/nope", "Bearer tok"},
	} {
		line := "GET " + tc.path + " HTTP/1.1\r\nHost: h\r\n"
		pairs := []string{":method", "GET", ":scheme", "http", ":path", tc.path, ":authority", "h"}
		if tc.auth != "" {
			line += "Authorization: " + tc.auth + "\r\n"
			pairs = append(pairs, "authorization", tc.auth)
		}
		var h1req corenet.HTTP1RequestValue
		var p http1.Parser
		if _, r := p.Parse([]byte(line+"\r\n"), &h1req); r != http1.RefusalNone {
			t.Fatal(r)
		}
		var h2req corenet.HTTP1RequestValue
		mapFields(&h2req, pairs...)
		if v := mapRequest(&h2req, st, 1); v.malformed || v.refusal != http1.RefusalNone {
			t.Fatalf("%s: %+v", tc.path, v)
		}
		if a, b := run(&h1req), run(&h2req); a != b {
			t.Errorf("%s %q: HTTP/1.1 %+v, HTTP/2 %+v", tc.path, tc.auth, a, b)
		}
	}
}
