package http1_test

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// refusalCase is one input the profile refuses, and the refusal it must name.
type refusalCase struct {
	name string
	in   string
	want http1.Refusal
}

// cRefRefusals are the refusal vectors of the C reference parser
// (bench/cref/src/parse_test.c, test_refusals), one for one. Where the C
// reference names a coarser reason, the comment says which: the decision —
// refused — is the same, and only the name is finer here.
var cRefRefusals = []refusalCase{
	{"bare LF line endings", "GET / HTTP/1.1\nHost: a\n\n", http1.RefusalBareLF},
	{"bare LF after a header", "GET / HTTP/1.1\r\nHost: a\n\r\n", http1.RefusalBareLF},
	{"bare CR after request line", "GET / HTTP/1.1\rHost: a\r\n\r\n", http1.RefusalBareCR},
	{"bare CR in a value", "GET / HTTP/1.1\r\nX: a\rb\r\n\r\n", http1.RefusalBareCR},
	{"bare CR ends the headers", "GET / HTTP/1.1\r\n\rX", http1.RefusalBareCR},
	{"NUL in a value", "GET / HTTP/1.1\r\nX: a\x00b\r\n\r\n", http1.RefusalNUL},
	{"obs-fold with space", "GET / HTTP/1.1\r\nX: a\r\n b\r\n\r\n", http1.RefusalObsFold},
	{"obs-fold with tab", "GET / HTTP/1.1\r\nX: a\r\n\tb\r\n\r\n", http1.RefusalObsFold},
	{"space before colon", "GET / HTTP/1.1\r\nHost : a\r\n\r\n", http1.RefusalSpaceBeforeColon},
	{"tab before colon", "GET / HTTP/1.1\r\nHost\t: a\r\n\r\n", http1.RefusalSpaceBeforeColon},
	{"TE chunked", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n", http1.RefusalTransferEncoding},
	{"TE any value, any case", "POST / HTTP/1.1\r\ntRaNsFeR-eNcOdInG: identity\r\n\r\n", http1.RefusalTransferEncoding},
	{"CL and TE", "POST / HTTP/1.1\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\nabcd", http1.RefusalTransferEncoding},
	{"TE with space before colon", "POST / HTTP/1.1\r\nTransfer-Encoding : chunked\r\n\r\n", http1.RefusalSpaceBeforeColon},
	{"CL empty", "POST / HTTP/1.1\r\nContent-Length:\r\n\r\n", http1.RefusalContentLength},
	{"CL signed", "POST / HTTP/1.1\r\nContent-Length: +5\r\n\r\n", http1.RefusalContentLength},
	{"CL negative", "POST / HTTP/1.1\r\nContent-Length: -1\r\n\r\n", http1.RefusalContentLength},
	{"CL hex", "POST / HTTP/1.1\r\nContent-Length: 0x10\r\n\r\n", http1.RefusalContentLength},
	{"CL inner space", "POST / HTTP/1.1\r\nContent-Length: 5 5\r\n\r\n", http1.RefusalContentLength},
	{"CL list", "POST / HTTP/1.1\r\nContent-Length: 5, 5\r\n\r\n", http1.RefusalContentLength},
	{"CL too long", "POST / HTTP/1.1\r\nContent-Length: 00000000001\r\n\r\n", http1.RefusalContentLength},
	{"CL overflow", "POST / HTTP/1.1\r\nContent-Length: 99999999999999999999\r\n\r\n", http1.RefusalContentLength},
	{"CL over the body cap", "POST / HTTP/1.1\r\nContent-Length: 1048577\r\n\r\n", http1.RefusalBodyTooLarge},
	// C: dup_content_length.
	{"CL repeated, divergent", "POST / HTTP/1.1\r\nContent-Length: 4\r\nContent-Length: 5\r\n\r\n", http1.RefusalContentLengthDuplicated},
	{"CL repeated, identical", "POST / HTTP/1.1\r\nContent-Length: 4\r\ncontent-length: 4\r\n\r\n", http1.RefusalContentLengthDuplicated},
	{"Authorization repeated", "GET / HTTP/1.1\r\nAuthorization: a\r\nauthorization: b\r\n\r\n", http1.RefusalAuthorizationDuplicated},
	{"Host repeated", "GET / HTTP/1.1\r\nHost: a\r\nHost: b\r\n\r\n", http1.RefusalHostDuplicated},
	{"name with non-tchar", "GET / HTTP/1.1\r\nX(y): a\r\n\r\n", http1.RefusalName},
	{"empty name", "GET / HTTP/1.1\r\n: a\r\n\r\n", http1.RefusalName},
	{"Kelvin sign is not K", "GET / HTTP/1.1\r\nContent-Length: 4\r\n\xe2\x84\xaa: x\r\n\r\n", http1.RefusalName},
	{"line without colon", "GET / HTTP/1.1\r\nHost\r\n\r\n", http1.RefusalName},
	{"HTTP/1.0", "GET / HTTP/1.0\r\n\r\n", http1.RefusalVersion},
	{"HTTP/2", "GET / HTTP/2.0\r\n\r\n", http1.RefusalVersion},
	{"lowercase version", "GET / http/1.1\r\n\r\n", http1.RefusalVersion},
	{"empty method", " / HTTP/1.1\r\n\r\n", http1.RefusalMethod},
	{"method with a separator", "G(T / HTTP/1.1\r\n\r\n", http1.RefusalMethod},
	{"empty target", "GET  HTTP/1.1\r\n\r\n", http1.RefusalTarget},
	{"control byte in target", "GET /a\x01 HTTP/1.1\r\n\r\n", http1.RefusalTarget},
	{"junk after version", "GET / HTTP/1.1 x\r\n\r\n", http1.RefusalRequestLine},
}

// profileRefusals are the refusals this profile adds to the C reference's,
// and the smuggling vectors of RFC 9112 §11.2 in the shapes they are sent.
var profileRefusals = []refusalCase{
	{"Expect 100-continue", "POST / HTTP/1.1\r\nContent-Length: 4\r\nExpect: 100-continue\r\n\r\nabcd", http1.RefusalExpect},
	{"Expect, any value", "GET / HTTP/1.1\r\nexpect: x\r\n\r\n", http1.RefusalExpect},
	{"CONNECT", "CONNECT example.test:443 HTTP/1.1\r\nHost: example.test:443\r\n\r\n", http1.RefusalMethodUnsupported},
	{"DEL in a value", "GET / HTTP/1.1\r\nX: a\x7fb\r\n\r\n", http1.RefusalControl},
	{"control byte in a value", "GET / HTTP/1.1\r\nX: a\x0bb\r\n\r\n", http1.RefusalControl},
	{"high byte in target", "GET /\xc3\xa9 HTTP/1.1\r\n\r\n", http1.RefusalTarget},
	{"DEL in target", "GET /\x7f HTTP/1.1\r\n\r\n", http1.RefusalTarget},
	{"NUL in a name", "GET / HTTP/1.1\r\nX\x00: a\r\n\r\n", http1.RefusalNUL},
	{"bare CR after a value, then LF", "GET / HTTP/1.1\r\nX: a\r\r\n\r\n", http1.RefusalBareCR},
	{"CR LF inside the version line", "GET / HTTP/1.1\r\r\n\r\n", http1.RefusalBareCR},
	{"TE chunked behind CL zero", "GET / HTTP/1.1\r\nContent-Length: 0\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n", http1.RefusalTransferEncoding},
	{"TE first, CL second", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\nContent-Length: 4\r\n\r\n", http1.RefusalTransferEncoding},
	{"TE with a tab before its value", "POST / HTTP/1.1\r\nTransfer-Encoding:\tchunked\r\n\r\n", http1.RefusalTransferEncoding},
	{"TE obs-folded", "POST / HTTP/1.1\r\nX: a\r\n Transfer-Encoding: chunked\r\n\r\n", http1.RefusalObsFold},
	{"TE in a trailer-like position", "POST / HTTP/1.1\r\nTrailer: X\r\nTransfer-Encoding: chunked\r\n\r\n", http1.RefusalTransferEncoding},
	{"CL zero then CL", "POST / HTTP/1.1\r\nContent-Length: 0\r\nContent-Length: 0\r\n\r\n", http1.RefusalContentLengthDuplicated},
	{"CL with trailing junk", "POST / HTTP/1.1\r\nContent-Length: 4x\r\n\r\nabcd", http1.RefusalContentLength},
	{"CL in exponent form", "POST / HTTP/1.1\r\nContent-Length: 1e3\r\n\r\n", http1.RefusalContentLength},
	{"CL with a NUL", "POST / HTTP/1.1\r\nContent-Length: 4\x00\r\n\r\nabcd", http1.RefusalNUL},
	{"Host with a space before colon", "GET / HTTP/1.1\r\nHost : evil\r\nHost: good\r\n\r\n", http1.RefusalSpaceBeforeColon},
	{"leading empty line", "\r\nGET / HTTP/1.1\r\n\r\n", http1.RefusalMethod},
	{"HTTP/0.9 shape", "GET /\r\n\r\n", http1.RefusalBareCR},
	{"absolute HTTP/1.1 prefix only", "GET / HTTP/1.12\r\n\r\n", http1.RefusalRequestLine},
}

func TestParseRefusals(t *testing.T) {
	t.Parallel()
	var p http1.Parser
	for _, set := range [][]refusalCase{cRefRefusals, profileRefusals} {
		for _, c := range set {
			var req corenet.HTTP1RequestValue
			n, r := p.Parse([]byte(c.in), &req)
			//: a refusal consumes nothing and names its rule.
			if n != 0 || r != c.want {
				t.Errorf("%s: Parse = %d, %s; want 0, %s", c.name, n, r, c.want)
			}
			//: whatever follows a refused prefix, the request stays refused.
			_, r2 := p.Parse([]byte(c.in+"GET / HTTP/1.1\r\n\r\n"), &req)
			if r2 != c.want {
				t.Errorf("%s: with a tail, Parse refuses %s; want %s", c.name, r2, c.want)
			}
		}
	}
}

// TestRefusalAnswers holds every refusal to a non-200 status, a core sentinel,
// and a canned answer with Connection: close: the one answer the codec gives.
func TestRefusalAnswers(t *testing.T) {
	t.Parallel()
	for r := http1.RefusalRequestLine; r <= http1.RefusalMethodUnsupported; r++ {
		st := r.Status()
		if st < 400 || st > 599 || r.Err() == nil || r.String() == "unknown" {
			t.Errorf("%d: status %d err %v name %s", r, st, r.Err(), r)
		}
		out := http1.AppendRefusal(nil, r)
		want := "HTTP/1.1 " + strconv.Itoa(st) + " "
		if !bytes.HasPrefix(out, []byte(want)) || !bytes.HasSuffix(out, []byte("\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")) {
			t.Errorf("%s: AppendRefusal = %q", r, out)
		}
	}
	if http1.RefusalNone.Status() != 0 || http1.RefusalNone.Err() != nil {
		t.Error("RefusalNone carries a status or an error")
	}
	if bad := http1.Refusal(200); bad.Status() != 0 || bad.Err() != nil || bad.String() != "unknown" {
		t.Error("an out-of-range Refusal answers as a known one")
	}
	if http1.RefusalExpect.Status() != 417 || http1.RefusalBodyTooLarge.Status() != 413 ||
		http1.RefusalTargetTooLong.Status() != 414 || http1.RefusalTooManyFields.Status() != 431 {
		t.Error("a size or Expect refusal is answered with the wrong status")
	}
}

func TestParseLimits(t *testing.T) {
	t.Parallel()
	var p http1.Parser
	head, tail := "GET / HTTP/1.1\r\nX: ", "\r\n\r\n"
	//: a head exactly at the bound is accepted, one byte over is refused.
	for over := range 2 {
		total := corenet.HTTP1DefaultHead + over
		in := head + strings.Repeat("v", total-len(head)-len(tail)) + tail
		var req corenet.HTTP1RequestValue
		n, r := p.Parse([]byte(in), &req)
		if over == 0 && (n != total || r != http1.RefusalNone) {
			t.Errorf("head at the bound: %d, %s", n, r)
		}
		if over == 1 && (n != 0 || r != http1.RefusalHeadTooLarge) {
			t.Errorf("head over the bound: %d, %s", n, r)
		}
	}
	//: a head that never ends is refused once the bound is spent.
	endless := "GET /" + strings.Repeat("a", corenet.HTTP1DefaultHead+64)
	var req corenet.HTTP1RequestValue
	if _, r := p.Parse([]byte(endless), &req); r != http1.RefusalTargetTooLong {
		t.Errorf("endless target: %s", r)
	}
	endlessValue := "GET / HTTP/1.1\r\nX: " + strings.Repeat("a", corenet.HTTP1DefaultHead+64)
	if _, r := p.Parse([]byte(endlessValue), &req); r != http1.RefusalHeadTooLarge {
		t.Errorf("endless value: %s", r)
	}
	//: one field line too many.
	var b strings.Builder
	b.WriteString("GET / HTTP/1.1\r\n")
	for i := range corenet.HTTP1MaxFields + 1 {
		fmt.Fprintf(&b, "X%d: v\r\n", i)
	}
	b.WriteString("\r\n")
	if _, r := p.Parse([]byte(b.String()), &req); r != http1.RefusalTooManyFields {
		t.Errorf("too many fields: %s", r)
	}
	//: exactly the maximum is accepted.
	full := strings.Replace(b.String(), fmt.Sprintf("X%d: v\r\n", corenet.HTTP1MaxFields), "", 1)
	if n, r := p.Parse([]byte(full), &req); n != len(full) || r != http1.RefusalNone || req.NFields != corenet.HTTP1MaxFields {
		t.Errorf("max fields: %d, %s, %d", n, r, req.NFields)
	}
	//: the target bound, at and past it.
	for over := range 2 {
		target := "/" + strings.Repeat("t", corenet.HTTP1DefaultTarget-1+over)
		in := "GET " + target + " HTTP/1.1\r\n\r\n"
		n, r := p.Parse([]byte(in), &req)
		if over == 0 && (n != len(in) || r != http1.RefusalNone) {
			t.Errorf("target at the bound: %d, %s", n, r)
		}
		if over == 1 && r != http1.RefusalTargetTooLong {
			t.Errorf("target over the bound: %s", r)
		}
	}
	//: the body bound, at and past it.
	at := "POST / HTTP/1.1\r\nContent-Length: " + strconv.FormatInt(corenet.HTTP1DefaultBody, 10) + "\r\n\r\n"
	if n, r := p.Parse([]byte(at), &req); n != 0 || r != http1.RefusalNone || req.Consumed != len(at)+int(corenet.HTTP1DefaultBody) {
		t.Errorf("body at the bound: %d, %s, consumed %d", n, r, req.Consumed)
	}
}

func TestNewParser(t *testing.T) {
	t.Parallel()
	p, err := http1.NewParser(corenet.HTTP1LimitsValue{Head: 128, Target: 16, Body: 10})
	if err != nil {
		t.Fatal(err)
	}
	if l := p.Limits(); l.Head != 128 || l.Target != 16 || l.Body != 10 {
		t.Fatalf("Limits = %+v", l)
	}
	var req corenet.HTTP1RequestValue
	if _, r := p.Parse([]byte("POST / HTTP/1.1\r\nContent-Length: 11\r\n\r\n"), &req); r != http1.RefusalBodyTooLarge {
		t.Errorf("body bound 10: %s", r)
	}
	if _, r := p.Parse([]byte("GET /"+strings.Repeat("a", 16)+" HTTP/1.1\r\n\r\n"), &req); r != http1.RefusalTargetTooLong {
		t.Errorf("target bound 16: %s", r)
	}
	if _, r := p.Parse([]byte("GET / HTTP/1.1\r\nX: "+strings.Repeat("a", 128)+"\r\n\r\n"), &req); r != http1.RefusalHeadTooLarge {
		t.Errorf("head bound 128: %s", r)
	}
	//: the zero value and the zero limits are the defaults.
	def, err := http1.NewParser(corenet.HTTP1LimitsValue{})
	if err != nil || def.Limits() != (http1.Parser{}).Limits() {
		t.Errorf("zero limits: %+v, %v", def.Limits(), err)
	}
	if l := (http1.Parser{}).Limits(); l.Head != corenet.HTTP1DefaultHead || l.Target != corenet.HTTP1DefaultTarget || l.Body != corenet.HTTP1DefaultBody {
		t.Errorf("zero Parser limits = %+v", l)
	}
	//: a small head pulls the default target under it.
	small, err := http1.NewParser(corenet.HTTP1LimitsValue{Head: 100})
	if err != nil || small.Limits().Target != 100 {
		t.Errorf("head 100: target %d, %v", small.Limits().Target, err)
	}
	for _, bad := range []corenet.HTTP1LimitsValue{
		{Head: -1},
		{Head: corenet.HTTP1MaxHead + 1},
		{Target: -1},
		{Head: 100, Target: 101},
		{Body: -1},
		{Body: corenet.HTTP1MaxBody + 1},
	} {
		if _, err := http1.NewParser(bad); !errs.HasCode(err, corenet.CodeHTTP1Misconfigured) {
			t.Errorf("NewParser(%+v) = %v; want HTTP1Misconfigured", bad, err)
		}
	}
}

func TestParseAccepts(t *testing.T) {
	t.Parallel()
	var p http1.Parser
	var req corenet.HTTP1RequestValue
	//: plain GET, case-folded names, OWS trimmed, a Connection token list.
	in := "GET /users/42 HTTP/1.1\r\nhOsT:  example.test \t\r\n" +
		"AUTHORIZATION:\tBearer abc.def.ghi\r\n" +
		"Connection: keep-alive, Close\r\nAccept: */*\r\n\r\n"
	n, r := p.Parse([]byte(in), &req)
	if n != len(in) || r != http1.RefusalNone || req.HeadLen != n || req.Consumed != n {
		t.Fatalf("plain GET: %d, %s", n, r)
	}
	if string(req.Method) != "GET" || string(req.Target) != "/users/42" {
		t.Errorf("request line: %q %q", req.Method, req.Target)
	}
	if !req.HasHost || string(req.Host) != "example.test" {
		t.Errorf("host: %v %q", req.HasHost, req.Host)
	}
	if !req.HasAuthorization || string(req.Authorization) != "Bearer abc.def.ghi" {
		t.Errorf("authorization: %q", req.Authorization)
	}
	if !req.Close || req.NFields != 4 {
		t.Errorf("close %v, fields %d", req.Close, req.NFields)
	}
	if v, ok := req.Field("accept"); !ok || string(v) != "*/*" {
		t.Errorf("Field(accept) = %q, %v", v, ok)
	}
	if v, ok := req.Field("ACCEPT"); !ok || string(v) != "*/*" {
		t.Errorf("Field(ACCEPT) = %q, %v", v, ok)
	}
	if _, ok := req.Field("missing"); ok {
		t.Error("Field(missing) found")
	}
	if _, ok := req.Field("acceptx"); ok {
		t.Error("Field(acceptx) found")
	}
	//: leading zeros and a body.
	in = "POST /x HTTP/1.1\r\nContent-Length: 0007\r\n\r\n1234567"
	if n, r = p.Parse([]byte(in), &req); n != len(in) || r != http1.RefusalNone || req.ContentLength != 7 || string(req.Body) != "1234567" {
		t.Errorf("leading zeros: %d, %s, %d, %q", n, r, req.ContentLength, req.Body)
	}
	//: a body not yet whole: the head is known, Continue finishes it.
	in = "POST /x HTTP/1.1\r\nContent-Length: 10\r\n\r\n12345"
	if n, r = p.Parse([]byte(in), &req); n != 0 || r != http1.RefusalNone || req.Consumed != req.HeadLen+10 || req.Body != nil {
		t.Errorf("partial body: %d, %s, %d/%d", n, r, req.Consumed, req.HeadLen)
	}
	if got := http1.Pending(&req, len(in)); got != 5 {
		t.Errorf("Pending = %d; want 5", got)
	}
	if got := http1.Continue([]byte(in), &req); got != 0 {
		t.Errorf("Continue on a partial body = %d", got)
	}
	in += "67890GET"
	if got := http1.Continue([]byte(in), &req); got != len(in)-3 || string(req.Body) != "1234567890" {
		t.Errorf("Continue = %d, %q", got, req.Body)
	}
	if got := http1.Pending(&req, len(in)); got != 0 {
		t.Errorf("Pending past the body = %d", got)
	}
	//: Continue with no head is a no-op.
	var fresh corenet.HTTP1RequestValue
	if http1.Continue([]byte("abc"), &fresh) != 0 {
		t.Error("Continue without a head")
	}
	//: keep-alive is not close; a value may carry high bytes and a colon; a tab inside a value stays.
	for _, c := range []struct{ in, field, want string }{
		{"GET / HTTP/1.1\r\nConnection: keep-alive\r\n\r\n", "connection", "keep-alive"},
		{"GET / HTTP/1.1\r\nX-A: a:b\xc3\xa9\r\n\r\n", "x-a", "a:b\xc3\xa9"},
		{"GET / HTTP/1.1\r\nX-A: a\tb \r\n\r\n", "x-a", "a\tb"},
		{"GET / HTTP/1.1\r\nX-A:\r\n\r\n", "x-a", ""},
		{"GET * HTTP/1.1\r\nHost:\r\n\r\n", "host", ""},
		{"OPTIONS http://a.test/x?y=1 HTTP/1.1\r\nConnection: closer\r\n\r\n", "connection", "closer"},
	} {
		if n, r = p.Parse([]byte(c.in), &req); n != len(c.in) || r != http1.RefusalNone {
			t.Errorf("%q: %d, %s", c.in, n, r)
			continue
		}
		if v, ok := req.Field(c.field); !ok || string(v) != c.want || req.Close {
			t.Errorf("%q: Field = %q, %v, close %v", c.in, v, ok, req.Close)
		}
	}
	//: an empty Host is present, and a request without Host is accepted (the C reference's rule).
	if n, r = p.Parse([]byte("GET * HTTP/1.1\r\nHost:\r\n\r\n"), &req); n == 0 || r != http1.RefusalNone || !req.HasHost || len(req.Host) != 0 {
		t.Error("empty Host not reported present")
	}
	if n, r = p.Parse([]byte("GET / HTTP/1.1\r\n\r\n"), &req); n == 0 || r != http1.RefusalNone || req.HasHost {
		t.Errorf("no Host: %d, %s", n, r)
	}
	//: a reused value forgets the previous request.
	p.Parse([]byte("GET / HTTP/1.1\r\nHost: a\r\nAuthorization: b\r\nConnection: close\r\nContent-Length: 1\r\n\r\nx"), &req)
	p.Parse([]byte("GET / HTTP/1.1\r\n\r\n"), &req)
	if req.HasHost || req.HasAuthorization || req.Close || req.ContentLength != 0 || req.NFields != 0 || len(req.Body) != 0 {
		t.Errorf("reused value kept state: %+v", req)
	}
}

// TestParsePrefixes holds the incremental contract: every strict prefix of a
// valid request needs more bytes, and only the whole request is accepted.
func TestParsePrefixes(t *testing.T) {
	t.Parallel()
	var p http1.Parser
	reqs := []string{
		"GET /users/1 HTTP/1.1\r\nHost: h\r\nAuthorization: Bearer x\r\n\r\n",
		"POST /users HTTP/1.1\r\nHost: h\r\nContent-Length: 12\r\n\r\n{\"a\":\"bcde\"}",
		"GET / HTTP/1.1\r\nX: a\tb\r\nConnection: close\r\n\r\n",
	}
	for k, in := range reqs {
		for n := 0; n <= len(in); n++ {
			var req corenet.HTTP1RequestValue
			got, r := p.Parse([]byte(in[:n]), &req)
			want := 0
			if n == len(in) {
				want = len(in)
			}
			if got != want || r != http1.RefusalNone {
				t.Errorf("request %d prefix %d/%d: %d, %s", k, n, len(in), got, r)
			}
		}
	}
}

// TestFraming holds the framing rule: whatever the method and the route, a
// request takes its head plus its Content-Length, so a body that spells a
// request is never handed back as one — a GET with a CL wrapping a request, a
// POST without credentials whose body is a request, a route that answers 404.
func TestFraming(t *testing.T) {
	t.Parallel()
	var p http1.Parser
	inner := "GET /admin HTTP/1.1\r\nHost: h\r\n\r\n"
	next := "GET /next HTTP/1.1\r\n\r\n"
	for _, m := range []string{"GET", "POST", "DELETE", "PUT", "HEAD", "OPTIONS"} {
		for _, route := range []string{"/users/1", "/no/such/route", "/"} {
			in := m + " " + route + " HTTP/1.1\r\nHost: h\r\nContent-Length: " + strconv.Itoa(len(inner)) + "\r\n\r\n" + inner + next
			var req corenet.HTTP1RequestValue
			n, r := p.Parse([]byte(in), &req)
			if r != http1.RefusalNone || n != req.HeadLen+len(inner) || string(req.Body) != inner {
				t.Errorf("%s %s: %d, %s, head %d", m, route, n, r, req.HeadLen)
				continue
			}
			//: the next request starts after the body, not inside it.
			n2, r2 := p.Parse([]byte(in[n:]), &req)
			if n2 != len(next) || r2 != http1.RefusalNone || string(req.Target) != "/next" {
				t.Errorf("%s %s: next request %d, %s, %q", m, route, n2, r2, req.Target)
			}
		}
	}
}

// TestEarlyAnswer covers the three early answers the plan names: a POST with
// no credentials whose body is a request, answered 401; a 404 with a body; and
// a GET whose CL wraps a request. Each drains exactly the declared body, and
// the stream resumes at the request after it.
func TestEarlyAnswer(t *testing.T) {
	t.Parallel()
	var p http1.Parser
	smuggled := "DELETE /users/1 HTTP/1.1\r\nAuthorization: Bearer stolen\r\n\r\n"
	follow := "GET /ok HTTP/1.1\r\n\r\n"
	for _, c := range []struct{ name, head string }{
		{"POST without auth, body is a request", "POST /users HTTP/1.1\r\nHost: h\r\nContent-Length: "},
		{"404 with a body", "PUT /no/such/route HTTP/1.1\r\nHost: h\r\nContent-Length: "},
		{"GET with a CL wrapping a request", "GET /users/1 HTTP/1.1\r\nHost: h\r\nContent-Length: "},
	} {
		head := c.head + strconv.Itoa(len(smuggled)) + "\r\n\r\n"
		//: only the head and half the body have arrived: answer early.
		stream := head + smuggled[:len(smuggled)/2]
		var req corenet.HTTP1RequestValue
		n, r := p.Parse([]byte(stream), &req)
		if n != 0 || r != http1.RefusalNone || req.HeadLen != len(head) {
			t.Fatalf("%s: head not framed: %d, %s", c.name, n, r)
		}
		drain := http1.Pending(&req, len(stream))
		if drain != len(smuggled)-len(smuggled)/2 {
			t.Errorf("%s: drain %d", c.name, drain)
		}
		//: the rest of the body and the next request arrive; the drain skips exactly the body.
		stream += smuggled[len(smuggled)/2:] + follow
		rest := stream[req.Consumed:]
		n2, r2 := p.Parse([]byte(rest), &req)
		if n2 != len(follow) || r2 != http1.RefusalNone || string(req.Target) != "/ok" {
			t.Errorf("%s: after the drain: %d, %s, %q", c.name, n2, r2, req.Target)
		}
	}
}

// TestPipelining frames a run of requests sent in one buffer.
func TestPipelining(t *testing.T) {
	t.Parallel()
	var p http1.Parser
	var b strings.Builder
	for i := range 50 {
		if i%3 == 0 {
			body := strings.Repeat("b", i)
			fmt.Fprintf(&b, "POST /r/%d HTTP/1.1\r\nHost: h\r\nContent-Length: %d\r\n\r\n%s", i, len(body), body)
		} else {
			fmt.Fprintf(&b, "GET /r/%d HTTP/1.1\r\nHost: h\r\n\r\n", i)
		}
	}
	buf := []byte(b.String())
	var req corenet.HTTP1RequestValue
	for i := range 50 {
		n, r := p.Parse(buf, &req)
		if n == 0 || r != http1.RefusalNone || string(req.Target) != "/r/"+strconv.Itoa(i) {
			t.Fatalf("request %d: %d, %s, %q", i, n, r, req.Target)
		}
		buf = buf[n:]
	}
	if len(buf) != 0 {
		t.Errorf("%d bytes left", len(buf))
	}
}
