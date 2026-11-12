//go:build !race

// The fixtures of http2's performance contracts (design/sdk.yaml, budgets):
// each sets one call up and returns it, and the gen_perf_test.go kit gen
// writes beside this file counts its allocations against its budget.
package http2_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// Sinks that keep a fixture's results alive.
var (
	perfHeader corehttp2.FrameValue
	perfInt    int
	perfCode   corehttp2.ErrCode
	perfBytes  []byte
	perfErr    error
)

// perfParseFrameHeader is ParseFrameHeader's fixture: a frame accepted and a
// frame refused for its length.
func perfParseFrameHeader(tb testing.TB) func() {
	buf := http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameHeaders, Length: 4096})
	big := http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameData, Length: 1 << 20})
	if _, n, code := http2.ParseFrameHeader(buf, 16384); n != 9 || code != corehttp2.ErrCodeNoError {
		tb.Fatalf("buf: %d %s", n, code)
	}
	return func() {
		perfHeader, perfInt, perfCode = http2.ParseFrameHeader(buf, 16384)
		perfHeader, perfInt, perfCode = http2.ParseFrameHeader(big, 16384)
	}
}

// perfRequestBlock is a request's field block in a first-seen, Huffman-free
// form, the kind every request of a keep-alive connection repeats.
func perfRequestBlock(tb testing.TB) []byte {
	tb.Helper()
	var block []byte
	block = append(block, 0x82, 0x86, 0x84) // :method GET, :scheme http, :path /
	block = appendLiteral(block, ":authority", "www.example.test")
	block = appendLiteral(block, "accept", "*/*")
	block = appendLiteral(block, "user-agent", "benchmark/1.0")
	block = appendLiteral(block, "x-request-id", "0123456789abcdef")
	block = appendLiteral(block, "accept-encoding", "gzip, deflate")
	return block
}

// appendLiteral writes a literal field without indexing, name and value as
// plain strings: the shape of a first request.
func appendLiteral(dst []byte, name, value string) []byte {
	dst = append(dst, 0x00)
	dst = append(dst, byte(len(name)))
	dst = append(dst, name...)
	dst = append(dst, byte(len(value)))
	dst = append(dst, value...)
	return dst
}

// perfDecoderDecode is Decode's fixture: a request of eight fields into a
// reused value and scratch, the table memory of the connection.
func perfDecoderDecode(tb testing.TB) func() {
	block := perfRequestBlock(tb)
	d := http2.NewDecoder(4096, 8192)
	mem := make([]byte, http2.TableMem(4096))
	var req corenet.HTTP1RequestValue
	scratch := make([]byte, 0, 4096)
	if _, err := d.Decode(mem, block, scratch, &req); err != nil {
		tb.Fatal(err)
	}
	return func() {
		perfBytes, perfErr = d.Decode(mem, block, scratch, &req)
	}
}

// perfEncoderAppendHeaders is AppendHeaders' fixture: a 200 with three fields
// and a body, into a buffer with room.
func perfEncoderAppendHeaders(tb testing.TB) func() {
	res := &corenet.HTTP1ResponseValue{
		Header: []byte("Content-Type: application/json\r\nCache-Control: no-store\r\nX-Request-Id: 0123456789abcdef\r\n"),
		Body:   []byte(`{"ok":true}`),
	}
	e := http2.NewEncoder(16384)
	dst := make([]byte, 0, 512)
	if _, err := e.AppendHeaders(dst, 1, res, false); err != nil {
		tb.Fatal(err)
	}
	return func() {
		perfBytes, perfErr = e.AppendHeaders(dst[:0], 1, res, false)
	}
}

// perfLaneFeed is Feed's fixture: sixteen HEADERS frames, one batch, one
// connection, sixteen streams answered and freed so the next batch starts
// from the same state.
func perfLaneFeed(tb testing.TB) func() {
	lane, err := http2.NewLane(corehttp2.LimitsValue{}, 1<<20, 0)
	if err != nil {
		tb.Fatal(err)
	}
	var c http2.Conn
	lane.Open(&c)
	preface := http2.AppendSettings([]byte(clientPrefaceText), nil)
	dst := make([]byte, 0, 1024)
	if n, _ := lane.Feed(&c, preface, dst); n != len(preface) {
		tb.Fatal("preface")
	}
	block := perfRequestBlock(tb)
	var stream uint32 = 1
	var buf []byte
	return func() {
		buf = buf[:0]
		for range 16 {
			buf = http2.AppendFrameHeader(buf, corehttp2.FrameValue{Type: corehttp2.FrameHeaders, Flags: corehttp2.FlagEndHeaders | corehttp2.FlagEndStream, StreamID: stream, Length: len(block)})
			buf = append(buf, block...)
			stream += 2
		}
		_, out := lane.Feed(&c, buf, dst[:0])
		perfBytes = out
		for _, ev := range lane.Events() {
			perfBytes, _ = lane.Headers(&c, ev.Stream, perfResponse, true, out[:0])
		}
		lane.EndBatch()
		stream = 1
		lane.Close(&c)
		lane.Open(&c)
		lane.Feed(&c, preface, dst[:0])
	}
}

// perfResponse is an empty response, the cheapest answer.
var perfResponse = &corenet.HTTP1ResponseValue{Status: 204}

// perfLaneAppendCredits is AppendCredits' fixture: sixteen streams with
// credit, a connection credit, one batch.
func perfLaneAppendCredits(tb testing.TB) func() {
	lane, err := http2.NewLane(corehttp2.LimitsValue{ReceivePerStream: 1 << 18, ReceivePerConn: 1 << 20}, 1<<20, 0)
	if err != nil {
		tb.Fatal(err)
	}
	var c http2.Conn
	lane.Open(&c)
	preface := http2.AppendSettings([]byte(clientPrefaceText), nil)
	dst := make([]byte, 0, 1024)
	if n, _ := lane.Feed(&c, preface, dst); n != len(preface) {
		tb.Fatal("preface")
	}
	block := perfRequestBlock(tb)
	var buf []byte
	for i := range 16 {
		buf = http2.AppendFrameHeader(buf, corehttp2.FrameValue{Type: corehttp2.FrameHeaders, Flags: corehttp2.FlagEndHeaders, StreamID: uint32(2*i + 1), Length: len(block)})
		buf = append(buf, block...)
	}
	if n, _ := lane.Feed(&c, buf, dst); n != len(buf) {
		tb.Fatal("feed")
	}
	return func() {
		for i := range 16 {
			lane.Consume(&c, uint32(2*i+1), 1<<16)
		}
		perfBytes = lane.AppendCredits(&c, dst[:0])
	}
}
