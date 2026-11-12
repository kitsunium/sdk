package http2_test

import (
	"bytes"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// Benchmark sinks that keep results alive.
var (
	benchHeader corehttp2.FrameValue
	benchInt    int
	benchCode   corehttp2.ErrCode
	benchBytes  []byte
	benchErr    error
)

// BenchmarkParseFrameHeader reads a frame's header: the budget of allocs: 0
// and the row of BENCH.md.
func BenchmarkParseFrameHeader(b *testing.B) {
	buf := http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameHeaders, Flags: corehttp2.FlagEndHeaders, StreamID: 1, Length: 128})
	b.ReportAllocs()
	for b.Loop() {
		benchHeader, benchInt, benchCode = http2.ParseFrameHeader(buf, 16384)
	}
}

// requestBlock8 is a GET of eight fields: three indexed pseudo-fields and five
// plain literals.
func requestBlock8() []byte {
	var block []byte
	block = append(block, 0x82, 0x86, 0x84)
	block = appendLit(block, ":authority", "www.example.test")
	block = appendLit(block, "accept", "*/*")
	block = appendLit(block, "user-agent", "benchmark/1.0")
	block = appendLit(block, "x-request-id", "0123456789abcdef")
	block = appendLit(block, "accept-encoding", "gzip, deflate")
	return block
}

// appendLit writes a literal without indexing, both strings plain.
func appendLit(dst []byte, name, value string) []byte {
	dst = append(dst, 0x00, byte(len(name)))
	dst = append(dst, name...)
	dst = append(dst, byte(len(value)))
	return append(dst, value...)
}

// BenchmarkDecode decodes a GET of eight fields, the row beside http1's
// Parse/get.
func BenchmarkDecode(b *testing.B) {
	block := requestBlock8()
	d := http2.NewDecoder(4096, 8192)
	mem := make([]byte, http2.TableMem(4096))
	var req corenet.HTTP1RequestValue
	scratch := make([]byte, 0, 1024)
	b.ReportAllocs()
	for b.Loop() {
		benchBytes, benchErr = d.Decode(mem, block, scratch, &req)
	}
}

// BenchmarkDecode_bomb is the expansion attack: a 16 KiB block referencing one
// 4 KiB dynamic entry 4 000 times, refused at the ceiling.
func BenchmarkDecode_bomb(b *testing.B) {
	value := string(bytes.Repeat([]byte("v"), 4096-32-1))
	var seed []byte
	seed = append(seed, 0x40, 1, 'x')
	seed = append(seed, 0x7f)
	n := len(value) - 127
	for n >= 128 {
		seed = append(seed, byte(n%128)|0x80)
		n /= 128
	}
	seed = append(seed, byte(n))
	seed = append(seed, value...)
	block := bytes.Repeat([]byte{0xbe}, 16384)
	d := http2.NewDecoder(4096, 8192)
	mem := make([]byte, http2.TableMem(4096))
	var req corenet.HTTP1RequestValue
	if _, err := d.Decode(mem, seed, nil, &req); err != nil {
		b.Fatal(err)
	}
	scratch := make([]byte, 0, 32768)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := d.Decode(mem, block, scratch, &req); err == nil {
			b.Fatal("the bomb was not refused")
		}
	}
}

// BenchmarkAppendHeaders writes a 200 with three fields and a content length.
func BenchmarkAppendHeaders(b *testing.B) {
	res := &corenet.HTTP1ResponseValue{
		Status: 200,
		Header: []byte("Content-Type: application/json\r\nCache-Control: no-store\r\nX-Request-Id: 0123456789abcdef\r\n"),
		Body:   []byte(`{"ok":true}`),
	}
	e := http2.NewEncoder(16384)
	dst := make([]byte, 0, 512)
	b.ReportAllocs()
	for b.Loop() {
		benchBytes, benchErr = e.AppendHeaders(dst[:0], 1, res, false)
	}
}

// BenchmarkFeedHeaders feeds sixteen HEADERS frames in one batch, a stream each,
// the state machine's cost per request: the row of #960 (ns per stream, 0
// allocations).
func BenchmarkFeedHeaders(b *testing.B) {
	lane, err := http2.NewLane(corehttp2.LimitsValue{}, 1<<20, 0)
	if err != nil {
		b.Fatal(err)
	}
	var c http2.Conn
	lane.Open(&c)
	dst := make([]byte, 0, 4096)
	preface := http2.AppendSettings([]byte(clientPrefaceText), nil)
	if n, _ := lane.Feed(&c, preface, dst[:0]); n != len(preface) {
		b.Fatal("preface")
	}
	block := requestBlock8()
	var frames []byte
	build := func() {
		frames = frames[:0]
		for i := range 16 {
			frames = http2.AppendFrameHeader(frames, corehttp2.FrameValue{
				Type: corehttp2.FrameHeaders, Flags: corehttp2.FlagEndHeaders | corehttp2.FlagEndStream,
				StreamID: uint32(2*i + 1), Length: len(block),
			})
			frames = append(frames, block...)
		}
	}
	build()
	res := &corenet.HTTP1ResponseValue{Status: 204}
	b.ReportAllocs()
	for b.Loop() {
		_, out := lane.Feed(&c, frames, dst[:0])
		for _, ev := range lane.Events() {
			out, _ = lane.Headers(&c, ev.Stream, res, true, out[:0])
		}
		benchBytes = out
		lane.EndBatch()
		lane.Close(&c)
		lane.Open(&c)
		lane.Feed(&c, preface, dst[:0])
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/16, "ns/stream")
}

// BenchmarkWindowUpdateCoalescing feeds 16 streams' DATA, consumes it, and
// writes the batch's credits: at most one WINDOW_UPDATE a stream and one for the
// connection (frames per batch reported).
func BenchmarkWindowUpdateCoalescing(b *testing.B) {
	lane, err := http2.NewLane(corehttp2.LimitsValue{ReceivePerStream: 1 << 18, ReceivePerConn: 1 << 21}, 1<<30, 0)
	if err != nil {
		b.Fatal(err)
	}
	var c http2.Conn
	lane.Open(&c)
	dst := make([]byte, 0, 4096)
	preface := http2.AppendSettings([]byte(clientPrefaceText), nil)
	if n, _ := lane.Feed(&c, preface, dst[:0]); n != len(preface) {
		b.Fatal("preface")
	}
	block := requestBlock8()
	chunk := bytes.Repeat([]byte{'x'}, 1024)
	var frames []byte
	for i := range 16 {
		frames = http2.AppendFrameHeader(frames, corehttp2.FrameValue{
			Type: corehttp2.FrameHeaders, Flags: corehttp2.FlagEndHeaders, StreamID: uint32(2*i + 1), Length: len(block),
		})
		frames = append(frames, block...)
		frames = http2.AppendFrameHeader(frames, corehttp2.FrameValue{Type: corehttp2.FrameData, StreamID: uint32(2*i + 1), Length: len(chunk)})
		frames = append(frames, chunk...)
	}
	if n, _ := lane.Feed(&c, frames, dst[:0]); n != len(frames) {
		b.Fatal("feed")
	}
	written := 0
	b.ReportAllocs()
	for b.Loop() {
		for i := range 16 {
			lane.Consume(&c, uint32(2*i+1), 1<<17)
		}
		out := lane.AppendCredits(&c, dst[:0])
		written = countFrames(out)
		benchBytes = out
		lane.EndBatch()
	}
	b.ReportMetric(float64(written), "frames/batch")
}

// countFrames counts the frames in out.
func countFrames(out []byte) int {
	count := 0
	for len(out) >= corehttp2.FrameHeaderLen {
		h, n, _ := http2.ParseFrameHeader(out, corehttp2.MaxFrameLimit)
		out = out[n+h.Length:]
		count++
	}
	return count
}
