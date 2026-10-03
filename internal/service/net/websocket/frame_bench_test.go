package websocket_test

import (
	"slices"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/websocket"
)

// benchMaskWordWidth mirrors the width ApplyMask moves at a time. The
// production constant is unexported, so the benchmark restates it — and the
// crossover row in BENCH.md is what would notice if the two ever disagreed.
const benchMaskWordWidth int = 2 * websocket.MaskLen

// benchHijackBuffer is the size of the bufio.Reader net/http hands to a
// hijacking handler, which is the largest frame that can arrive in one buffered
// read.
const benchHijackBuffer int = 4096

// sinks so no frame, mask or validation can be proven unused and elided.
var (
	bytesSink  []byte
	headerSink websocket.FrameHeaderValue
	intSink    int
	strSink    string
	codeSink   corenet.WSCloseCode
	errSink    error
)

// benchMaskKey is the four-byte key RFC 6455 requires a client to prefix to
// every frame. A server therefore XORs every inbound byte with it.
var benchMaskKey = [websocket.MaskLen]byte{0xDE, 0xAD, 0xBE, 0xEF}

// benchPayload builds a payload of n bytes, outside every timed loop.
func benchPayload(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte('a' + i%26)
	}
	return out
}

// benchClientFrame assembles a MASKED client frame by hand, because that is the
// only direction ParseFrameHeader accepts: RFC 6455 §5.1 requires a
// client-to-server frame to be masked and requires a server to fail the
// connection on an unmasked one, so AppendFrame — which writes the SERVER's
// unmasked direction — produces bytes the parser refuses. Writing this
// benchmark tripped over exactly that, which is the requirement being enforced
// rather than documented.
func benchClientFrame(b *testing.B, payload []byte) []byte {
	b.Helper()
	//: FIN set, binary opcode.
	wire := []byte{0x80 | byte(corenet.WSBinary)}
	//: MASK bit set, then the payload length in the width the RFC prescribes.
	switch n := len(payload); {
	case n < 126:
		wire = append(wire, 0x80|byte(n))
	case n <= 0xFFFF:
		wire = append(wire, 0x80|126, byte(n>>8), byte(n))
	default:
		b.Fatalf("benchClientFrame: %d bytes needs the 64-bit length form", n)
	}
	wire = append(wire, benchMaskKey[:]...)
	masked := slices.Clone(payload)
	websocket.ApplyMask(masked, benchMaskKey)
	return append(wire, masked...)
}

// BenchmarkApplyMask_* is the per-byte cost every inbound message pays. RFC
// 6455 requires a CLIENT frame to be masked and a server to refuse an unmasked
// one, so this XOR is not optional and not skippable: it runs over the whole
// payload of every message a browser sends.
func BenchmarkApplyMask_64B(b *testing.B)  { benchMask(b, 64) }
func BenchmarkApplyMask_4KiB(b *testing.B) { benchMask(b, 4<<10) }
func BenchmarkApplyMask_1MiB(b *testing.B) { benchMask(b, 1<<20) }

func benchMask(b *testing.B, n int) {
	b.Helper()
	payload := benchPayload(n)
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		websocket.ApplyMask(payload, benchMaskKey)
	}
	bytesSink = payload
}

// maskBenchLengths are the lengths that expose how the transform handles what
// its wide loops cannot take.
//
// They are not round numbers for tidiness. ApplyMask moves a thirty-two-byte
// block, then a word, then a byte, so seven, fifteen and thirty-one are the
// worst case of each tier — a full seven bytes left to the slowest loop — while
// eight, thirty-two and sixty-four are the best. WSMaxControlPayload is RFC
// 6455 §5.5's ceiling on a control frame, so it is the largest payload a Ping
// can carry and the common case for a heartbeat. Four thousand and ninety-six
// is net/http's hijacked bufio.Reader, and one past it is the first size whose
// frame cannot arrive in a single buffered read.
var maskBenchLengths = []int{
	0, 1, 3, 4, 7, benchMaskWordWidth, 15, 64,
	corenet.WSMaxControlPayload, benchHijackBuffer, benchHijackBuffer + 1, 64 << 10, 1 << 20,
}

// BenchmarkApplyMask measures the shipped implementation against the
// byte-at-a-time form it replaced — IN THE SAME BINARY, on the same lengths and
// the same payloads.
//
// Publishing a before number from one run and an after number from another
// invites a machine-state difference to be read as a speed-up. Here the two
// rows are minutes apart at most, on the same CPU with the same buffer, so the
// ratio between them is measured rather than inferred. `byte_at_a_time` calls
// applyMaskReference, which is the previous production body verbatim and is
// also the correctness oracle in websocket_frame_external_test.go — so the
// thing being timed is exactly the thing being proved equivalent.
func BenchmarkApplyMask(b *testing.B) {
	//: the shipped word-at-a-time transform.
	for _, n := range maskBenchLengths {
		b.Run("wide/"+benchLengthName(n), func(b *testing.B) {
			benchMaskWith(b, n, websocket.ApplyMask)
		})
	}
	//: the form it replaced, for the ratio.
	for _, n := range maskBenchLengths {
		b.Run("byte_at_a_time/"+benchLengthName(n), func(b *testing.B) {
			benchMaskWith(b, n, applyMaskReference)
		})
	}
	//: the rejected alternative — see benchMaskShortGuarded.
	for _, n := range maskBenchLengths {
		b.Run("short_guarded/"+benchLengthName(n), func(b *testing.B) {
			benchMaskWith(b, n, benchMaskShortGuarded)
		})
	}
}

// benchMaskShortGuarded is NOT the shipped implementation and must never
// become it. It is the short-payload branch the wide transform invites — below
// one word, skip the set-up and go straight to the byte loop — kept here so the
// decision NOT to ship it stays measurable instead of becoming folklore.
//
// The wide form is slower than the byte-at-a-time one below eight bytes, which
// is a real cost on a path that carries empty Pings. This variant recovers it.
// What the rows show is that it does not pay: the extra branch is paid on every
// call, including the payloads between one word and one cache line where the
// wide form's whole win begins, and the amount recovered below one word is
// smaller than the amount lost above it. A control frame is capped at
// WSMaxControlPayload by §5.5, so the sizes this would help are precisely the
// sizes that are already too cheap to matter.
func benchMaskShortGuarded(payload []byte, key [websocket.MaskLen]byte) {
	//: below one word there is nothing for the wide loops to take, so the key
	//: word would be built and thrown away.
	if len(payload) < benchMaskWordWidth {
		for i := range payload {
			payload[i] ^= key[i&(websocket.MaskLen-1)]
		}
		//: handled.
		return
	}
	websocket.ApplyMask(payload, key)
}

func benchMaskWith(b *testing.B, n int, apply func([]byte, [websocket.MaskLen]byte)) {
	b.Helper()
	payload := benchPayload(n)
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		apply(payload, benchMaskKey)
	}
	bytesSink = payload
}

// benchLengthName renders a length as a fixed-width decimal so the sub-benchmark
// names sort in the order the lengths do, which is what makes the generated
// table readable without re-sorting it by hand.
func benchLengthName(n int) string {
	name := itoaMask(n)
	for len(name) < 7 {
		name = "0" + name
	}
	return name
}

// BenchmarkValidateText_* is the other per-byte pass. ADR 0047 judges UTF-8
// on the REASSEMBLED message, because a rune may straddle a fragment and a
// per-frame check would reject valid input — so this runs once over the whole
// message, and its throughput is what caps a text-heavy connection.
func BenchmarkValidateText_64B(b *testing.B)  { benchValidate(b, 64) }
func BenchmarkValidateText_4KiB(b *testing.B) { benchValidate(b, 4<<10) }
func BenchmarkValidateText_1MiB(b *testing.B) { benchValidate(b, 1<<20) }

func benchValidate(b *testing.B, n int) {
	b.Helper()
	payload := benchPayload(n)
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = websocket.ValidateText(payload)
	}
	if errSink != nil {
		b.Fatalf("ValidateText: %v", errSink)
	}
}

// BenchmarkValidateText_Multibyte is the same length in bytes but built from
// three-byte runes, so the validator cannot take an ASCII fast path. The delta
// against the ASCII row is what a non-Latin conversation costs.
func BenchmarkValidateText_Multibyte(b *testing.B) {
	payload := []byte(strings.Repeat("日", (4<<10)/3))
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = websocket.ValidateText(payload)
	}
	if errSink != nil {
		b.Fatalf("ValidateText: %v", errSink)
	}
}

// BenchmarkParseFrameHeader and BenchmarkFrameHeaderLen are per FRAME
// rather than per byte, so they are the fixed cost of the protocol. ADR 0047
// checks every ceiling against the length the PEER announced, before any
// allocation, and that check lives here.
func BenchmarkFrameHeaderLen(b *testing.B) {
	wire := benchClientFrame(b, benchPayload(4<<10))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		intSink = websocket.FrameHeaderLen(wire)
	}
}

func BenchmarkParseFrameHeader(b *testing.B) {
	wire := benchClientFrame(b, benchPayload(4<<10))
	//: the parser takes EXACTLY the header bytes and refuses a longer slice, so
	//: it cannot over-read into a payload it has not validated the length of.
	//: That is the contract a connection follows — FrameHeaderLen first, then
	//: read that many — and the benchmark follows it too.
	header := wire[:websocket.FrameHeaderLen(wire)]
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		headerSink, errSink = websocket.ParseFrameHeader(header)
	}
	if errSink != nil {
		b.Fatalf("ParseFrameHeader: %v", errSink)
	}
}

// BenchmarkAppendFrame_* is the outbound half: the server writes an unmasked
// frame, so this is a header plus a copy. Reusing the destination buffer is the
// documented way to avoid an allocation per message, and these rows are what
// makes that worth saying.
func BenchmarkAppendFrame_64B(b *testing.B)  { benchAppend(b, 64) }
func BenchmarkAppendFrame_4KiB(b *testing.B) { benchAppend(b, 4<<10) }

func benchAppend(b *testing.B, n int) {
	b.Helper()
	payload := benchPayload(n)
	dst := make([]byte, 0, n+16)
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = websocket.AppendFrame(dst[:0], corenet.WSBinary, true, payload)
	}
	if errSink != nil {
		b.Fatalf("AppendFrame: %v", errSink)
	}
}

// BenchmarkAppendClosePayload and BenchmarkParseClosePayload cover the
// close handshake, once per connection.
func BenchmarkAppendClosePayload(b *testing.B) {
	dst := make([]byte, 0, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = websocket.AppendClosePayload(dst[:0], corenet.WSCloseNormal, "done")
	}
	if errSink != nil {
		b.Fatalf("AppendClosePayload: %v", errSink)
	}
}

func BenchmarkParseClosePayload(b *testing.B) {
	wire, err := websocket.AppendClosePayload(nil, corenet.WSCloseNormal, "done")
	if err != nil {
		b.Fatalf("AppendClosePayload: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		codeSink, strSink, errSink = websocket.ParseClosePayload(wire)
	}
	if errSink != nil {
		b.Fatalf("ParseClosePayload: %v", errSink)
	}
}
