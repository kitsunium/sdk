package server

import (
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// bigFieldBlock is a GET whose one extra field's value is n bytes: an HPACK
// literal, its value length past the 7-bit prefix.
func bigFieldBlock(n int) []byte {
	b := requestBlock("GET", "/big")
	b = append(b, 0x00, 3, 'x', '-', 'b')
	v := n - 127
	b = append(b, 0x7f)
	for v >= 128 {
		b = append(b, byte(v%128)|0x80)
		v /= 128
	}
	b = append(b, byte(v))
	for range n {
		b = append(b, 'v')
	}
	return b
}

// waitListener polls State until ok holds for the group's first listener row,
// or fails after a bound.
func waitListener(t *testing.T, srv *Server, what string, ok func(corenet.ListenerStateValue) bool) corenet.ListenerStateValue {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		row := srv.State().Listeners[0]
		if ok(row) {
			return row
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: listener row %+v", what, row)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// errorCount is the count of typ in row's HTTP2Errors.
func errorCount(row corenet.ListenerStateValue, typ corehttp2.ErrorType) uint64 {
	for _, e := range row.HTTP2Errors {
		if e.Type == typ {
			return e.Count
		}
	}
	return 0
}

// TestLoopHTTP2_counts holds #966 on the loop engine, every backend: one
// HTTP/1.1 and one HTTP/2 request count one each on the group's row, a header
// list past its bound counts header_list once, and a SETTINGS frame whose
// length is no multiple of six counts frame_size once.
func TestLoopHTTP2_counts(t *testing.T) {
	hello := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("ok") })
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			srv, addr := startLoopServer(t, b, http2.HandleLoop(hello, http1.Parser{}), Plaintext())
			assertHTTP1OK(t, addr)
			c := dialRawH2(t, addr)
			c.get(1, "/")
			if got := c.answerOf(1); got != "ok" {
				t.Fatalf("h2 answer %q", got)
			}
			row := waitListener(t, srv, "requests", func(r corenet.ListenerStateValue) bool {
				return r.HTTP1Requests == 1 && r.HTTP2Requests == 1
			})
			if len(row.HTTP2Errors) != 0 {
				t.Fatalf("errors counted for two clean requests: %+v", row.HTTP2Errors)
			}
			big := dialRawH2(t, addr)
			big.send(corehttp2.FrameHeaders, corehttp2.FlagEndStream|corehttp2.FlagEndHeaders, 1, bigFieldBlock(corehttp2.DefaultMaxHeaderList+100))
			if code := big.untilGoAway(); code != corehttp2.ErrCodeCompression {
				t.Fatalf("header list past its bound: GOAWAY %v", code)
			}
			waitListener(t, srv, "header_list", func(r corenet.ListenerStateValue) bool {
				return errorCount(r, corehttp2.ErrorTypeHeaderList) == 1
			})
			bad := dialRawH2(t, addr)
			bad.send(corehttp2.FrameSettings, 0, 0, make([]byte, 5))
			if code := bad.untilGoAway(); code != corehttp2.ErrCodeFrameSize {
				t.Fatalf("SETTINGS of 5 bytes: GOAWAY %v", code)
			}
			row = waitListener(t, srv, "frame_size", func(r corenet.ListenerStateValue) bool {
				return errorCount(r, corehttp2.ErrorTypeFrameSize) == 1
			})
			if errorCount(row, corehttp2.ErrorTypeHeaderList) != 1 || len(row.HTTP2Errors) != 2 {
				t.Fatalf("errors %+v, want header_list 1 and frame_size 1", row.HTTP2Errors)
			}
		})
	}
}
