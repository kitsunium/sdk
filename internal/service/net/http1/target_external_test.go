package http1_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// TestCanonicalTarget pins the one target every link of a chain reads: the
// path up to the query for origin-form, the path of an absolute-form target
// once its authority matched Host, "*" for OPTIONS, and a 400 for every path
// two readers could resolve apart — so a router and an authenticating link
// never judge two different resources.
func TestCanonicalTarget(t *testing.T) {
	t.Parallel()
	accepted := []struct{ line, host, path, query string }{
		{"GET / HTTP/1.1", "h", "/", ""},
		{"GET /a/b HTTP/1.1", "h", "/a/b", ""},
		{"GET /a/b/ HTTP/1.1", "h", "/a/b/", ""},
		{"GET /a?x=1&y=%2F HTTP/1.1", "h", "/a", "x=1&y=%2F"},
		{"GET /a? HTTP/1.1", "h", "/a", ""},
		{"GET /caf%C3%A9/%41 HTTP/1.1", "h", "/caf%C3%A9/%41", ""},
		{"GET /a..b/.c/d. HTTP/1.1", "h", "/a..b/.c/d.", ""},
		{"OPTIONS * HTTP/1.1", "h", "*", ""},
		{"GET http://h/a/b?q HTTP/1.1", "h", "/a/b", "q"},
		{"GET HTTP://H:8080/a HTTP/1.1", "h:8080", "/a", ""},
		{"GET https://h HTTP/1.1", "h", "/", ""},
		{"GET http://h?q=1 HTTP/1.1", "h", "/", "q=1"},
	}
	var p http1.Parser
	var req corenet.HTTP1RequestValue
	for _, c := range accepted {
		in := c.line + "\r\nHost: " + c.host + "\r\n\r\n"
		if n, r := p.Parse([]byte(in), &req); n != len(in) || r != http1.RefusalNone {
			t.Errorf("%q: %d, %s", c.line, n, r)
			continue
		}
		if string(req.Path) != c.path || string(req.Query) != c.query {
			t.Errorf("%q: Path %q Query %q, want %q %q", c.line, req.Path, req.Query, c.path, c.query)
		}
	}
	refused := []struct {
		line, host string
		want       http1.Refusal
	}{
		{"GET /a/../b HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET /a/./b HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET /.. HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET /a/. HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET /a/..?x HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET //a HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET /a//b HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET /a\\b HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET /a%2fb HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET /a%2Fb HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET /%2e%2e/b HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET /a%2E HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET /a%5cb HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET /a%00 HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET /a%zz HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET /a%2 HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET /a% HTTP/1.1", "h", http1.RefusalTargetPath},
		{"GET * HTTP/1.1", "h", http1.RefusalTargetForm},
		{"GET a/b HTTP/1.1", "h", http1.RefusalTargetForm},
		{"GET ftp://h/a HTTP/1.1", "h", http1.RefusalTargetForm},
		{"GET http://other/a HTTP/1.1", "h", http1.RefusalTargetHost},
		{"GET http://u@h/a HTTP/1.1", "h", http1.RefusalTargetHost},
		{"GET http:///a HTTP/1.1", "h", http1.RefusalTargetHost},
		{"GET http://h/../a HTTP/1.1", "h", http1.RefusalTargetPath},
	}
	for _, c := range refused {
		in := c.line + "\r\nHost: " + c.host + "\r\n\r\n"
		if n, r := p.Parse([]byte(in), &req); n != 0 || r != c.want || r.Status() != 400 {
			t.Errorf("%q: %d, %s, want %s (400)", c.line, n, r, c.want)
		}
	}
	//: absolute-form needs a Host to match.
	if _, r := p.Parse([]byte("GET http://h/a HTTP/1.1\r\n\r\n"), &req); r != http1.RefusalTargetHost {
		t.Errorf("absolute-form without Host: %s", r)
	}
	//: the refusals are the codec's sentinel for a malformed request.
	for _, r := range []http1.Refusal{http1.RefusalTargetPath, http1.RefusalTargetForm, http1.RefusalTargetHost} {
		if r.Err() != corenet.HTTP1RequestMalformed {
			t.Errorf("%s: Err = %v", r, r.Err())
		}
	}
}

// TestParse_resetsWhatALinkSets pins that Parse clears Route, Auth and
// Authenticated, so nothing a link set on one request reaches the next one
// framed into the same value.
func TestParse_resetsWhatALinkSets(t *testing.T) {
	t.Parallel()
	var p http1.Parser
	var req corenet.HTTP1RequestValue
	req.Route, req.Authenticated = 7, true
	req.Auth.SetSubject([]byte("alice"))
	req.Auth.SetClaims(3)
	in := []byte("GET /x HTTP/1.1\r\nHost: h\r\n\r\n")
	if n, r := p.Parse(in, &req); n != len(in) || r != http1.RefusalNone {
		t.Fatalf("Parse: %d, %s", n, r)
	}
	if req.Route != 0 || req.Authenticated || len(req.Auth.Subject()) != 0 || req.Auth.Claims() != 0 {
		t.Fatalf("left %d %v %q %d", req.Route, req.Authenticated, req.Auth.Subject(), req.Auth.Claims())
	}
}

// TestCanonicalTarget_pathIsTheRequests pins that the root of an
// absolute-form target and the asterisk alias the request's own bytes: a link
// that writes into one request's Path leaves the next request's as it was,
// whatever connection or lane it arrives on.
func TestCanonicalTarget_pathIsTheRequests(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ line, path string }{
		{"OPTIONS * HTTP/1.1", "*"},
		{"GET http://h HTTP/1.1", "/"},
		{"GET https://h?q HTTP/1.1", "/"},
	} {
		in := c.line + "\r\nHost: h\r\n\r\n"
		var first, next corenet.HTTP1RequestValue
		if n, r := (http1.Parser{}).Parse([]byte(in), &first); n != len(in) || r != http1.RefusalNone {
			t.Fatalf("%q: %d, %s", c.line, n, r)
		}
		first.Path[0] = 'X'
		if n, r := (http1.Parser{}).Parse([]byte(in), &next); n != len(in) || r != http1.RefusalNone {
			t.Fatalf("%q: %d, %s", c.line, n, r)
		}
		if string(next.Path) != c.path || cap(next.Path) != 1 {
			t.Errorf("%q: after a write into an earlier Path, Path %q (cap %d), want %q", c.line, next.Path, cap(next.Path), c.path)
		}
	}
}

// TestCanonical_exported holds the exported reader to the parser's: every
// accepted and refused target of the table comes out of Canonical with the
// same verdict and the same Path and Query as out of Parse, for a request
// whose fields a codec filled itself.
func TestCanonical_exported(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		method, target, host string
		want                 http1.Refusal
		path, query          string
	}{
		{"GET", "/a/b", "h", http1.RefusalNone, "/a/b", ""},
		{"GET", "/a?x=1&y=%2F", "h", http1.RefusalNone, "/a", "x=1&y=%2F"},
		{"GET", "/caf%C3%A9/%41", "h", http1.RefusalNone, "/caf%C3%A9/%41", ""},
		{"OPTIONS", "*", "h", http1.RefusalNone, "*", ""},
		{"GET", "http://h/a/b?q", "h", http1.RefusalNone, "/a/b", "q"},
		{"GET", "/a/../b", "h", http1.RefusalTargetPath, "", ""},
		{"GET", "/a//b", "h", http1.RefusalTargetPath, "", ""},
		{"GET", "/a%2Fb", "h", http1.RefusalTargetPath, "", ""},
		{"GET", "*", "h", http1.RefusalTargetForm, "", ""},
		{"GET", "http://x/a", "h", http1.RefusalTargetHost, "", ""},
		{"GET", "", "h", http1.RefusalTarget, "", ""},
	} {
		req := corenet.HTTP1RequestValue{Method: []byte(tc.method), Target: []byte(tc.target), Host: []byte(tc.host), HasHost: true}
		if r := http1.Canonical(&req); r != tc.want {
			t.Errorf("%s %q: refusal %s, want %s", tc.method, tc.target, r, tc.want)
			continue
		}
		if tc.want != http1.RefusalNone {
			continue
		}
		if string(req.Path) != tc.path || string(req.Query) != tc.query {
			t.Errorf("%s %q: Path %q Query %q, want %q %q", tc.method, tc.target, req.Path, req.Query, tc.path, tc.query)
		}
		if len(req.Path) > 0 && len(req.Target) > 0 && tc.target != "*" && tc.path != "/" && &req.Path[0] != &req.Target[len(req.Target)-len(tc.path)-queryTail(tc.query)] {
			t.Errorf("%q: Path does not alias Target", tc.target)
		}
	}
}

// queryTail is the bytes of a target past its path: "?" and the query.
func queryTail(query string) int {
	if query == "" {
		return 0
	}
	return len(query) + 1
}

// TestParse_setsProto pins what Parse leaves for a chain link: HTTP/1.1's
// major version, no scheme and no stream.
func TestParse_setsProto(t *testing.T) {
	t.Parallel()
	var p http1.Parser
	req := corenet.HTTP1RequestValue{Scheme: []byte("stale"), Stream: 9, Proto: 2}
	if _, r := p.Parse([]byte("GET / HTTP/1.1\r\nHost: h\r\n\r\n"), &req); r != http1.RefusalNone {
		t.Fatal(r)
	}
	if req.Proto != 1 || req.Stream != 0 || req.Scheme != nil {
		t.Fatalf("Proto %d Stream %d Scheme %q after Parse", req.Proto, req.Stream, req.Scheme)
	}
}
