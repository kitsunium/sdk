package http1_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// perfInt, perfRefusal and perfBytes keep the results alive so the compiler cannot drop a call.
var (
	perfInt     int
	perfRefusal http1.Refusal
	perfBytes   []byte
)

// benchGet and benchPost are the challenge API's two request shapes; the
// pipelined input is sixteen GETs in one read.
var (
	benchGet  = []byte("GET /users/12345 HTTP/1.1\r\nHost: 127.0.0.1:3000\r\nUser-Agent: k6/2.3.0 (https://k6.io/)\r\nAuthorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NSIsIm5hbWUiOiJ1c2VyIiwiZXhwIjo0MTAyNDQ0ODAwfQ.c2lnbmF0dXJlc2lnbmF0dXJlc2lnbmF0dXJlc2ln\r\nAccept-Encoding: gzip\r\n\r\n")
	benchPost = []byte("POST /users HTTP/1.1\r\nHost: 127.0.0.1:3000\r\nUser-Agent: k6/2.3.0 (https://k6.io/)\r\nContent-Type: application/json\r\nContent-Length: 44\r\nAuthorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln\r\n\r\n{\"username\":\"user_12345\",\"email\":\"u@e.test\"}")
	benchPipe = bytes.Repeat(benchGet, 16)
)

func BenchmarkParse(b *testing.B) {
	var p http1.Parser
	var req corenet.HTTP1RequestValue
	for _, c := range []struct {
		name string
		in   []byte
	}{{"get", benchGet}, {"post", benchPost}, {"refused", []byte("POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\n\r\n")}} {
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(int64(len(c.in)))
			b.ReportAllocs()
			for b.Loop() {
				perfInt, perfRefusal = p.Parse(c.in, &req)
			}
		})
	}
	b.Run("pipelined16", func(b *testing.B) {
		b.SetBytes(int64(len(benchPipe)))
		b.ReportAllocs()
		for b.Loop() {
			buf := benchPipe
			for len(buf) > 0 {
				n, _ := p.Parse(buf, &req)
				buf = buf[n:]
			}
		}
	})
}

// BenchmarkNetHTTPReadRequest is the competitor: what the default engine pays
// to read the same requests through net/http, body included.
func BenchmarkNetHTTPReadRequest(b *testing.B) {
	for _, c := range []struct {
		name string
		in   []byte
	}{{"get", benchGet}, {"post", benchPost}} {
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(int64(len(c.in)))
			b.ReportAllocs()
			rd := bytes.NewReader(c.in)
			br := bufio.NewReader(rd)
			for b.Loop() {
				rd.Reset(c.in)
				br.Reset(rd)
				r, err := http.ReadRequest(br)
				if err != nil {
					b.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, r.Body)
			}
		})
	}
}

func BenchmarkAppendResponse(b *testing.B) {
	req := &corenet.HTTP1RequestValue{Method: []byte("GET")}
	hdr, _ := http1.AppendField(nil, "Content-Type", "application/json")
	res := &corenet.HTTP1ResponseValue{Header: hdr, Body: []byte(`{"id":12345,"username":"user_12345","email":"u@e.test"}`)}
	dst := make([]byte, 0, 512)
	b.ReportAllocs()
	for b.Loop() {
		perfBytes, _ = http1.AppendResponse(dst[:0], req, res, false)
	}
}

func BenchmarkAppendRefusal(b *testing.B) {
	dst := make([]byte, 0, 128)
	b.ReportAllocs()
	for b.Loop() {
		perfBytes = http1.AppendRefusal(dst[:0], http1.RefusalBareLF)
	}
}

// crefStream is the C reference's benchmark stream (bench/cref/src/parse_bench.c,
// build_stream): 32 GET and POST pairs, pipelined, each carrying a 188-byte
// bearer token — here a run of letters of the same length, since a scan's cost
// does not depend on which letters it reads.
func crefStream() []byte {
	tok := strings.Repeat("a", 188)
	body := `{"name":"Jean Dupont","email":"jean@example.test"}`
	var b strings.Builder
	for i := range 32 {
		fmt.Fprintf(&b, "GET /users/%d HTTP/1.1\r\nHost: 10.0.0.7:3000\r\n"+
			"User-Agent: k6/1.0\r\nAccept: */*\r\nAccept-Encoding: gzip\r\n"+
			"Authorization: Bearer %s\r\nConnection: keep-alive\r\n\r\n", i, tok)
		fmt.Fprintf(&b, "POST /users HTTP/1.1\r\nHost: 10.0.0.7:3000\r\n"+
			"User-Agent: k6/1.0\r\nContent-Type: application/json\r\n"+
			"Authorization: Bearer %s\r\nContent-Length: %d\r\nConnection: keep-alive\r\n\r\n%s", tok, len(body), body)
	}
	return []byte(b.String())
}

// BenchmarkParseCRefStream parses the C reference's stream, so its ns/op
// divided by 64 compares with the C harness's ns/request on the same bytes.
func BenchmarkParseCRefStream(b *testing.B) {
	stream := crefStream()
	var p http1.Parser
	var req corenet.HTTP1RequestValue
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	for b.Loop() {
		buf := stream
		for len(buf) > 0 {
			n, r := p.Parse(buf, &req)
			if n == 0 {
				b.Fatalf("refused: %s", r)
			}
			buf = buf[n:]
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*64), "ns/request")
}
