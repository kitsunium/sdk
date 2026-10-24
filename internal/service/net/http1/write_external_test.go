package http1_test

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strconv"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

func TestAppendStatusLine(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		status int
		want   string
	}{
		{200, "HTTP/1.1 200 OK\r\n"},
		{201, "HTTP/1.1 201 Created\r\n"},
		{404, "HTTP/1.1 404 Not Found\r\n"},
		{431, "HTTP/1.1 431 Request Header Fields Too Large\r\n"},
		{299, "HTTP/1.1 299 \r\n"},
		{599, "HTTP/1.1 599 \r\n"},
		{999, "HTTP/1.1 999 \r\n"},
	} {
		got, err := http1.AppendStatusLine([]byte("x"), c.status)
		if err != nil || string(got) != "x"+c.want {
			t.Errorf("AppendStatusLine(%d) = %q, %v", c.status, got, err)
		}
	}
	//: every registered line agrees with the standard library's reason phrase,
	//: but where RFC 9110 renamed it and the standard library kept the old name.
	rfc9110 := map[int]string{413: "Content Too Large", 414: "URI Too Long", 416: "Range Not Satisfiable", 422: "Unprocessable Content"}
	for code := 200; code < 600; code++ {
		got, _ := http1.AppendStatusLine(nil, code)
		text := http.StatusText(code)
		if renamed, ok := rfc9110[code]; ok {
			text = renamed
		}
		if text != "" && string(got) != "HTTP/1.1 "+strconv.Itoa(code)+" "+text+"\r\n" && string(got) != "HTTP/1.1 "+strconv.Itoa(code)+" \r\n" {
			t.Errorf("%d: %q vs %q", code, got, text)
		}
	}
	for _, bad := range []int{-1, 0, 100, 101, 199, 1000} {
		got, err := http1.AppendStatusLine([]byte("x"), bad)
		if !errs.HasCode(err, corenet.CodeHTTP1ResponseInvalid) || string(got) != "x" {
			t.Errorf("AppendStatusLine(%d) = %q, %v", bad, got, err)
		}
	}
}

func TestAppendField(t *testing.T) {
	t.Parallel()
	got, err := http1.AppendField(nil, "Content-Type", "application/json")
	if err != nil || string(got) != "Content-Type: application/json\r\n" {
		t.Errorf("AppendField = %q, %v", got, err)
	}
	if got, err = http1.AppendField(nil, "X", "a\tb"); err != nil || string(got) != "X: a\tb\r\n" {
		t.Errorf("tab value = %q, %v", got, err)
	}
	for _, c := range []struct{ name, value string }{
		{"", "v"},
		{"X Y", "v"},
		{"X:", "v"},
		{"Content-Length", "1"},
		{"connection", "close"},
		{"Transfer-Encoding", "chunked"},
		{"X", "a\r\nInjected: 1"},
		{"X", "a\nb"},
		{"X", "a\x00"},
		{"X", "\x7f"},
	} {
		got, err := http1.AppendField([]byte("x"), c.name, c.value)
		if !errs.HasCode(err, corenet.CodeHTTP1ResponseInvalid) || string(got) != "x" {
			t.Errorf("AppendField(%q, %q) = %q, %v", c.name, c.value, got, err)
		}
	}
}

// readResponse reads out with net/http's response reader, for req's method.
func readResponse(t *testing.T, out []byte, method string) (*http.Response, []byte) {
	t.Helper()
	br := bufio.NewReader(bytes.NewReader(out))
	res, err := http.ReadResponse(br, &http.Request{Method: method})
	if err != nil {
		t.Fatalf("%q: %v", out, err)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%q: body %v", out, err)
	}
	if br.Buffered() != 0 {
		t.Fatalf("%q: %d bytes left after the response", out, br.Buffered())
	}
	return res, body
}

func TestAppendResponse(t *testing.T) {
	t.Parallel()
	get := &corenet.HTTP1RequestValue{Method: []byte("GET")}
	hdr, _ := http1.AppendField(nil, "Content-Type", "application/json")
	res := &corenet.HTTP1ResponseValue{Header: hdr, Body: []byte(`{"id":1}`)}
	out, err := http1.AppendResponse(nil, get, res, false)
	want := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 8\r\n\r\n{\"id\":1}"
	if err != nil || string(out) != want {
		t.Fatalf("AppendResponse = %q, %v", out, err)
	}
	hr, body := readResponse(t, out, "GET")
	if hr.StatusCode != 200 || string(body) != `{"id":1}` || hr.Close {
		t.Errorf("net/http read %d %q close %v", hr.StatusCode, body, hr.Close)
	}
	//: any party closes: the request, the response, the caller.
	for _, c := range []struct {
		reqClose, resClose, closing bool
	}{{true, false, false}, {false, true, false}, {false, false, true}} {
		req := &corenet.HTTP1RequestValue{Method: []byte("POST"), Close: c.reqClose}
		r := &corenet.HTTP1ResponseValue{Status: 401, Body: []byte("no"), Close: c.resClose}
		out, err := http1.AppendResponse(nil, req, r, c.closing)
		if err != nil {
			t.Fatal(err)
		}
		hr, body := readResponse(t, out, "POST")
		if hr.StatusCode != 401 || !hr.Close || string(body) != "no" {
			t.Errorf("%+v: %d close %v %q", c, hr.StatusCode, hr.Close, body)
		}
	}
	//: HEAD: the length of the body it does not send.
	head := &corenet.HTTP1RequestValue{Method: []byte("HEAD")}
	out, _ = http1.AppendResponse(nil, head, &corenet.HTTP1ResponseValue{Body: []byte("12345")}, false)
	if string(out) != "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\n" {
		t.Errorf("HEAD = %q", out)
	}
	readResponse(t, out, "HEAD")
	//: 204 and 304: neither a length nor a body.
	for _, st := range []int{204, 304} {
		out, err := http1.AppendResponse(nil, get, &corenet.HTTP1ResponseValue{Status: st}, false)
		if err != nil || bytes.Contains(out, []byte("Content-Length")) {
			t.Errorf("%d = %q, %v", st, out, err)
		}
		readResponse(t, out, "GET")
		out, err = http1.AppendResponse([]byte("x"), get, &corenet.HTTP1ResponseValue{Status: st, Body: []byte("b")}, false)
		if !errs.HasCode(err, corenet.CodeHTTP1ResponseInvalid) || string(out) != "x" {
			t.Errorf("%d with a body = %q, %v", st, out, err)
		}
	}
	//: an interim status appends nothing.
	out, err = http1.AppendResponse([]byte("x"), get, &corenet.HTTP1ResponseValue{Status: 100}, false)
	if !errs.HasCode(err, corenet.CodeHTTP1ResponseInvalid) || string(out) != "x" {
		t.Errorf("100 = %q, %v", out, err)
	}
	//: a 404 with a body, pipelined behind a 200: both framed.
	out, _ = http1.AppendResponse(nil, get, &corenet.HTTP1ResponseValue{Status: 404, Body: []byte(`{"error":"not found"}`)}, false)
	out, _ = http1.AppendResponse(out, get, &corenet.HTTP1ResponseValue{Body: []byte("ok")}, false)
	br := bufio.NewReader(bytes.NewReader(out))
	for _, w := range []struct {
		status int
		body   string
	}{{404, `{"error":"not found"}`}, {200, "ok"}} {
		hr, err := http.ReadResponse(br, &http.Request{Method: "GET"})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(hr.Body)
		if hr.StatusCode != w.status || string(b) != w.body {
			t.Errorf("pipelined: %d %q", hr.StatusCode, b)
		}
	}
}

// TestAppendRefusalReadable holds every canned refusal to net/http's reader.
func TestAppendRefusalReadable(t *testing.T) {
	t.Parallel()
	for r := http1.RefusalNone; r <= http1.RefusalMethodUnsupported+1; r++ {
		out := http1.AppendRefusal(nil, r)
		hr, body := readResponse(t, out, "GET")
		want := r.Status()
		if want == 0 {
			want = 400
		}
		if hr.StatusCode != want || !hr.Close || len(body) != 0 || hr.ContentLength != 0 {
			t.Errorf("%s: %d close %v", r, hr.StatusCode, hr.Close)
		}
	}
}
