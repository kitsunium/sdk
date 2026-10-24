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

// fuzzSeeds are the corpus's inputs, accepted and refused, and two requests
// long enough to cross the word scans.
func fuzzSeeds() []string {
	seeds := []string{
		"GET /users/42 HTTP/1.1\r\nHost: example.test\r\nAuthorization: Bearer abc.def.ghi\r\nConnection: keep-alive\r\nAccept: */*\r\n\r\n",
		"POST /users HTTP/1.1\r\nHost: h\r\nContent-Length: 12\r\nX-Long: 0123456789abcdef0123456789abcdef0123456789\r\n\r\n{\"a\":\"bcde\"}",
		"GET /a HTTP/1.1\r\nHost: h\r\nContent-Length: 5\r\n\r\nhelloGET /b HTTP/1.1\r\nHost: h\r\n\r\n",
	}
	for _, set := range [][]refusalCase{cRefRefusals, profileRefusals} {
		for _, c := range set {
			seeds = append(seeds, c.in)
		}
	}
	return seeds
}

// checkProperties holds, for one input, every property the codec promises
// whatever the bytes are.
func checkProperties(t *testing.T, p http1.Parser, in []byte) {
	t.Helper()
	var req corenet.HTTP1RequestValue
	n, r := p.Parse(in, &req)
	//: one of the three outcomes, never two.
	if n != 0 && r != http1.RefusalNone {
		t.Fatalf("%q: consumed %d and refused %s", in, n, r)
	}
	if n > 0 && (n > len(in) || req.HeadLen > n || n != req.Consumed || int64(n-req.HeadLen) != req.ContentLength) {
		t.Fatalf("%q: n %d head %d consumed %d cl %d", in, n, req.HeadLen, req.Consumed, req.ContentLength)
	}
	//: every slice lies inside the input.
	if r == http1.RefusalNone {
		for _, s := range [][]byte{req.Method, req.Target, req.Host, req.Authorization, req.Body} {
			if !inside(in, s) {
				t.Fatalf("%q: a slice outside the input", in)
			}
		}
		for i := range req.NFields {
			if !inside(in, req.Fields[i].Name) || !inside(in, req.Fields[i].Value) {
				t.Fatalf("%q: field %d outside the input", in, i)
			}
		}
	}
	//: the answer never depends on the bytes past len: the capacity holds a head's end.
	roomy := make([]byte, len(in), len(in)+16)
	copy(roomy, in)
	copy(roomy[len(in):cap(roomy)], "\r\n\r\n\r\n\r\n\r\n\r\n\r\n\r\n")
	var req2 corenet.HTTP1RequestValue
	if n2, r2 := p.Parse(roomy, &req2); n2 != n || r2 != r || req2.HeadLen != req.HeadLen {
		t.Fatalf("%q: the capacity changed the answer: %d/%s vs %d/%s", in, n2, r2, n, r)
	}
	//: a refused prefix refuses the whole input with the same rule; an accepted
	//: prefix frames the same request whatever follows it.
	step := 1 + len(in)/512
	for k := 0; k < len(in); k += step {
		var pr corenet.HTTP1RequestValue
		pn, prr := p.Parse(in[:k], &pr)
		if prr != http1.RefusalNone && r != prr {
			t.Fatalf("%q: prefix %d refused %s, the whole input %d/%s", in, k, prr, n, r)
		}
		if pn > 0 && (n != pn || req.HeadLen != pr.HeadLen) {
			t.Fatalf("%q: prefix %d took %d, the whole input %d/%s", in, k, pn, n, r)
		}
	}
}

// inside reports whether s is empty or lies within base.
func inside(base, s []byte) bool {
	if len(s) == 0 {
		return true
	}
	for i := 0; i+len(s) <= len(base); i++ {
		if &base[i] == &s[0] {
			return true
		}
	}
	return false
}

func FuzzParseProperties(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add([]byte(s))
	}
	var p http1.Parser
	small, err := http1.NewParser(corenet.HTTP1LimitsValue{Head: 96, Target: 24, Body: 16})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		checkProperties(t, p, in)
		checkProperties(t, small, in)
	})
}

// TestMutations is the C reference's deterministic mutation fuzz (xorshift,
// the same seed and the same interesting bytes), run on every test pass so the
// properties hold without -fuzz. A coverage build runs a tenth of the
// mutations: checkProperties parses every prefix of each input, and under
// `bazel coverage` the full 20000 took 15 s on a workstation and past the 60 s
// .bazelrc gives the target on CI. The race suite, the alloc lane and `go test`
// run them all; the tenth still reaches every statement.
func TestMutations(t *testing.T) {
	t.Parallel()
	state := uint64(0x2545f4914f6cdd1d)
	next := func() uint64 {
		state ^= state >> 12
		state ^= state << 25
		state ^= state >> 27
		return state * 0x2545f4914f6cdd1d
	}
	seeds := fuzzSeeds()[:3]
	interesting := []byte{'\r', '\n', 0, ' ', '\t', ':', 'a', '0', 0x7f, 0x80, 0xff}
	var p http1.Parser
	iters := 20000
	if testing.Short() || testing.CoverMode() != "" {
		iters = 2000
	}
	for range iters {
		in := []byte(seeds[next()%uint64(len(seeds))])
		for range 1 + next()%3 {
			at := next() % uint64(len(in))
			c := interesting[next()%uint64(len(interesting))]
			switch next() % 3 {
			case 0:
				in[at] = c
			case 1:
				in = append(in[:at], append([]byte{c}, in[at:]...)...)
			default:
				if len(in) > 1 {
					in = append(in[:at], in[at+1:]...)
				}
			}
		}
		if next()%4 == 0 {
			in = in[:next()%uint64(len(in)+1)]
		}
		checkProperties(t, p, in)
	}
}

// FuzzDifferential compares the codec with net/http's request reader on what
// the codec accepts. The codec is the stricter of the two by design, so a
// request net/http refuses and the codec accepts must belong to a class listed
// here; one that does not is a finding. Where both accept, they must frame the
// same bytes and read the same method, target, Host, length and close.
func FuzzDifferential(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add([]byte(s))
	}
	var p http1.Parser
	f.Fuzz(func(t *testing.T, in []byte) {
		var req corenet.HTTP1RequestValue
		n, r := p.Parse(in, &req)
		if r != http1.RefusalNone || n == 0 {
			return
		}
		src := bytes.NewReader(in)
		br := bufio.NewReader(src)
		hr, err := http.ReadRequest(br)
		if err != nil {
			if !netHTTPStricter(&req, err) {
				t.Fatalf("%q: the codec accepts, net/http refuses: %v", in, err)
			}
			return
		}
		body, err := io.ReadAll(hr.Body)
		if err != nil {
			t.Fatalf("%q: net/http body: %v", in, err)
		}
		//: what net/http left is what bufio holds plus what it never read: an
		//: input past bufio's 4096 bytes is not all in the buffer.
		left := br.Buffered() + src.Len()
		if len(in)-left != n {
			t.Fatalf("%q: the codec takes %d bytes, net/http %d", in, n, len(in)-left)
		}
		if hr.Method != string(req.Method) || hr.RequestURI != string(req.Target) ||
			hr.ContentLength != req.ContentLength || !bytes.Equal(body, req.Body) || hr.Close != req.Close {
			t.Fatalf("%q: net/http read %s %s cl %d close %v", in, hr.Method, hr.RequestURI, hr.ContentLength, hr.Close)
		}
		if strings.HasPrefix(hr.RequestURI, "/") && hr.Host != string(req.Host) {
			t.Fatalf("%q: Host %q vs %q", in, hr.Host, req.Host)
		}
	})
}

// netHTTPStricter lists the classes where net/http refuses a request the
// profile accepts, each a rule the profile deliberately does not apply.
func netHTTPStricter(req *corenet.HTTP1RequestValue, err error) bool {
	msg := err.Error()
	switch {
	case !req.HasHost && strings.Contains(msg, "missing required Host header"):
		// RFC 9112 §3.2's Host requirement: the profile serves one origin and
		// routes by target, and the C reference accepts a request without Host.
		return true
	case strings.Contains(msg, "malformed Host header"):
		// net/http checks the Host value's syntax; the profile carries it.
		return true
	case strings.Contains(msg, "invalid URI") || strings.Contains(msg, "parse "):
		// net/http parses the target as a URL; the profile carries it as sent.
		return true
	case strings.Contains(msg, "too many Host headers"):
		// unreachable: the profile refuses a second Host.
		return false
	}
	return false
}
