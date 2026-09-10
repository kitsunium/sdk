package net_test

import (
	"slices"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// sinks so no frame, mask or validation can be proven unused and elided.
var (
	bytesSink  []byte
	headerSink corenet.WSFrameHeaderValue
	intSink    int
	strSink    string
	codeSink   corenet.WSCloseCode
	errSink    error
)

// benchMaskKey is the four-byte key RFC 6455 requires a client to prefix to
// every frame. A server therefore XORs every inbound byte with it.
var benchMaskKey = [corenet.WSMaskLen]byte{0xDE, 0xAD, 0xBE, 0xEF}

// benchPayload builds a payload of n bytes, outside every timed loop.
func benchPayload(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte('a' + i%26)
	}
	return out
}

// benchClientFrame assembles a MASKED client frame by hand, because that is the
// only direction ParseWSFrameHeader accepts: RFC 6455 §5.1 requires a
// client-to-server frame to be masked and requires a server to fail the
// connection on an unmasked one, so AppendWSFrame — which writes the SERVER's
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
	corenet.ApplyWSMask(masked, benchMaskKey)
	return append(wire, masked...)
}

// BenchmarkApplyWSMask_* is the per-byte cost every inbound message pays. RFC
// 6455 requires a CLIENT frame to be masked and a server to refuse an unmasked
// one, so this XOR is not optional and not skippable: it runs over the whole
// payload of every message a browser sends.
func BenchmarkApplyWSMask_64B(b *testing.B)  { benchMask(b, 64) }
func BenchmarkApplyWSMask_4KiB(b *testing.B) { benchMask(b, 4<<10) }
func BenchmarkApplyWSMask_1MiB(b *testing.B) { benchMask(b, 1<<20) }

func benchMask(b *testing.B, n int) {
	b.Helper()
	payload := benchPayload(n)
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		corenet.ApplyWSMask(payload, benchMaskKey)
	}
	bytesSink = payload
}

// BenchmarkValidateWSText_* is the other per-byte pass. ADR 0047 judges UTF-8
// on the REASSEMBLED message, because a rune may straddle a fragment and a
// per-frame check would reject valid input — so this runs once over the whole
// message, and its throughput is what caps a text-heavy connection.
func BenchmarkValidateWSText_64B(b *testing.B)  { benchValidate(b, 64) }
func BenchmarkValidateWSText_4KiB(b *testing.B) { benchValidate(b, 4<<10) }
func BenchmarkValidateWSText_1MiB(b *testing.B) { benchValidate(b, 1<<20) }

func benchValidate(b *testing.B, n int) {
	b.Helper()
	payload := benchPayload(n)
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = corenet.ValidateWSText(payload)
	}
	if errSink != nil {
		b.Fatalf("ValidateWSText: %v", errSink)
	}
}

// BenchmarkValidateWSText_Multibyte is the same length in bytes but built from
// three-byte runes, so the validator cannot take an ASCII fast path. The delta
// against the ASCII row is what a non-Latin conversation costs.
func BenchmarkValidateWSText_Multibyte(b *testing.B) {
	payload := []byte(strings.Repeat("日", (4<<10)/3))
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = corenet.ValidateWSText(payload)
	}
	if errSink != nil {
		b.Fatalf("ValidateWSText: %v", errSink)
	}
}

// BenchmarkParseWSFrameHeader and BenchmarkWSFrameHeaderLen are per FRAME
// rather than per byte, so they are the fixed cost of the protocol. ADR 0047
// checks every ceiling against the length the PEER announced, before any
// allocation, and that check lives here.
func BenchmarkWSFrameHeaderLen(b *testing.B) {
	wire := benchClientFrame(b, benchPayload(4<<10))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		intSink = corenet.WSFrameHeaderLen(wire)
	}
}

func BenchmarkParseWSFrameHeader(b *testing.B) {
	wire := benchClientFrame(b, benchPayload(4<<10))
	//: the parser takes EXACTLY the header bytes and refuses a longer slice, so
	//: it cannot over-read into a payload it has not validated the length of.
	//: That is the contract a connection follows — WSFrameHeaderLen first, then
	//: read that many — and the benchmark follows it too.
	header := wire[:corenet.WSFrameHeaderLen(wire)]
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		headerSink, errSink = corenet.ParseWSFrameHeader(header)
	}
	if errSink != nil {
		b.Fatalf("ParseWSFrameHeader: %v", errSink)
	}
}

// BenchmarkAppendWSFrame_* is the outbound half: the server writes an unmasked
// frame, so this is a header plus a copy. Reusing the destination buffer is the
// documented way to avoid an allocation per message, and these rows are what
// makes that worth saying.
func BenchmarkAppendWSFrame_64B(b *testing.B)  { benchAppend(b, 64) }
func BenchmarkAppendWSFrame_4KiB(b *testing.B) { benchAppend(b, 4<<10) }

func benchAppend(b *testing.B, n int) {
	b.Helper()
	payload := benchPayload(n)
	dst := make([]byte, 0, n+16)
	b.SetBytes(int64(n))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = corenet.AppendWSFrame(dst[:0], corenet.WSBinary, true, payload)
	}
	if errSink != nil {
		b.Fatalf("AppendWSFrame: %v", errSink)
	}
}

// BenchmarkAppendWSClosePayload and BenchmarkParseWSClosePayload cover the
// close handshake, once per connection.
func BenchmarkAppendWSClosePayload(b *testing.B) {
	dst := make([]byte, 0, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = corenet.AppendWSClosePayload(dst[:0], corenet.WSCloseNormal, "done")
	}
	if errSink != nil {
		b.Fatalf("AppendWSClosePayload: %v", errSink)
	}
}

func BenchmarkParseWSClosePayload(b *testing.B) {
	wire, err := corenet.AppendWSClosePayload(nil, corenet.WSCloseNormal, "done")
	if err != nil {
		b.Fatalf("AppendWSClosePayload: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		codeSink, strSink, errSink = corenet.ParseWSClosePayload(wire)
	}
	if errSink != nil {
		b.Fatalf("ParseWSClosePayload: %v", errSink)
	}
}
