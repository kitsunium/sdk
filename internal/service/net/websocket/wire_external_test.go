// Package websocket_test — the WebSocket wire format, checked against RFC 6455
// itself rather than against this implementation's own idea of it. These
// tests moved here from internal/core/net with the codec they check
// (ADR 0160 §4); the protocol's values keep theirs in the core.
//
// Every case below cites the section it comes from. Where the RFC prints a
// worked example — §1.3's handshake, §5.7's frames — the example is the
// expectation verbatim, because an implementation that agrees with itself
// proves nothing.
package websocket_test

import (
	"bytes"
	"encoding/binary"
	"slices"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/websocket"
)

// TestAcceptKeyMatchesTheWorkedExample pins §1.3's own numbers.
//
// The whole point of the accept value is that it cannot be produced by echoing
// the request, so the only honest test is one written from the RFC's example
// and not from this code.
func TestAcceptKeyMatchesTheWorkedExample(t *testing.T) {
	t.Parallel()
	//: RFC 6455 §1.3 — key "dGhlIHNhbXBsZSBub25jZQ==" yields this accept.
	const key = "dGhlIHNhbXBsZSBub25jZQ=="
	const want = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if got := websocket.AcceptKey(key); got != want {
		t.Fatalf("AcceptKey(%q) = %q, want %q (RFC 6455 §1.3)", key, got, want)
	}
}

// TestValidateKeyRefusesWhatIsNotANonce covers §4.1's requirement that the
// key be base64 of exactly sixteen bytes.
func TestValidateKeyRefusesWhatIsNotANonce(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		key  string
		ok   bool
	}
	tests := []tc{
		{"the RFC's own example", "dGhlIHNhbXBsZSBub25jZQ==", true},
		{"absent", "", false},
		{"not base64", "!!!!not base64!!!!", false},
		{"eight bytes", "AAAAAAAAAAA=", false},
		{"twenty-four bytes", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := websocket.ValidateKey(c.key)
			if c.ok && err != nil {
				t.Fatalf("ValidateKey(%q) = %v, want nil", c.key, err)
			}
			if !c.ok {
				if err == nil {
					t.Fatalf("ValidateKey(%q) = nil, want a refusal", c.key)
				}
				if !errs.HasCode(err, corenet.CodeWSHandshakeFailed) {
					t.Fatalf("ValidateKey(%q) code = %v, want WS_HANDSHAKE_FAILED", c.key, wsCodeOf(err))
				}
			}
		})
	}
}

// TestAppendFrameMatchesTheWorkedExamples pins §5.7's frames byte for byte,
// including the two extended length forms.
//
// The length encodings are the part every implementation gets subtly wrong, so
// they are checked against the RFC's own hex rather than against a round trip
// through this package's parser — which would agree with any consistent bug.
func TestAppendFrameMatchesTheWorkedExamples(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		op      corenet.WSOpCode
		final   bool
		payload []byte
		want    []byte
	}
	big := bytes.Repeat([]byte{0xAA}, 256)
	huge := bytes.Repeat([]byte{0xBB}, 65536)
	tests := []tc{
		{
			//: §5.7 — "a single-frame unmasked text message".
			name: "single-frame unmasked text", op: corenet.WSText, final: true,
			payload: []byte("Hello"),
			want:    []byte{0x81, 0x05, 0x48, 0x65, 0x6c, 0x6c, 0x6f},
		},
		{
			//: §5.7 — the first half of "a fragmented unmasked text message".
			name: "first fragment", op: corenet.WSText, final: false,
			payload: []byte("Hel"),
			want:    []byte{0x01, 0x03, 0x48, 0x65, 0x6c},
		},
		{
			//: §5.7 — the second half; the opcode is continuation, FIN is set.
			name: "final continuation", op: corenet.WSContinuation, final: true,
			payload: []byte("lo"),
			want:    []byte{0x80, 0x02, 0x6c, 0x6f},
		},
		{
			//: §5.7 — "unmasked Ping request".
			name: "unmasked ping", op: corenet.WSPing, final: true,
			payload: []byte("Hello"),
			want:    []byte{0x89, 0x05, 0x48, 0x65, 0x6c, 0x6c, 0x6f},
		},
		{
			//: §5.7 — "256 bytes binary message in a single unmasked frame".
			name: "256-byte binary uses the 16-bit length", op: corenet.WSBinary, final: true,
			payload: big,
			want:    append([]byte{0x82, 0x7E, 0x01, 0x00}, big...),
		},
		{
			//: §5.7 — "64KiB binary message in a single unmasked frame".
			name: "64KiB binary uses the 64-bit length", op: corenet.WSBinary, final: true,
			payload: huge,
			want:    append([]byte{0x82, 0x7F, 0, 0, 0, 0, 0, 0x01, 0, 0}, huge...),
		},
		{
			//: the boundary the 7-bit field can still express.
			name: "125 bytes still fits the 7-bit length", op: corenet.WSBinary, final: true,
			payload: bytes.Repeat([]byte{1}, 125),
			want:    append([]byte{0x82, 125}, bytes.Repeat([]byte{1}, 125)...),
		},
		{
			//: one byte past it, which must switch to the 16-bit form.
			name: "126 bytes switches to the 16-bit length", op: corenet.WSBinary, final: true,
			payload: bytes.Repeat([]byte{1}, 126),
			want:    append([]byte{0x82, 0x7E, 0x00, 0x7E}, bytes.Repeat([]byte{1}, 126)...),
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := websocket.AppendFrame(nil, c.op, c.final, c.payload)
			if err != nil {
				t.Fatalf("AppendFrame: %v", err)
			}
			if !bytes.Equal(got, c.want) {
				t.Fatalf("frame = % x\nwant   = % x", got, c.want)
			}
			//: the server must never mask, so the mask bit is always clear.
			if got[1]&0x80 != 0 {
				t.Fatalf("the server masked a frame; RFC 6455 §5.1 forbids it")
			}
		})
	}
}

// TestAppendFrameLeavesTheBufferUntouchedOnRefusal pins the property a frame
// stream depends on: a refused frame must not leave half of itself on a wire
// the peer is already parsing.
func TestAppendFrameLeavesTheBufferUntouchedOnRefusal(t *testing.T) {
	t.Parallel()
	existing := []byte{0xDE, 0xAD}
	//: a control frame past the §5.5 ceiling.
	got, err := websocket.AppendFrame(existing, corenet.WSPing, true, bytes.Repeat([]byte{0}, 126))
	if err == nil {
		t.Fatalf("AppendFrame accepted a 126-byte ping; RFC 6455 §5.5 caps it at 125")
	}
	if !bytes.Equal(got, existing) {
		t.Fatalf("buffer = % x, want it untouched (% x)", got, existing)
	}
}

// TestApplyMaskDecodesTheWorkedExample pins §5.3's transform against §5.7's
// masked frame, and pins that it is its own inverse.
func TestApplyMaskDecodesTheWorkedExample(t *testing.T) {
	t.Parallel()
	//: §5.7 — "a single-frame masked text message" containing "Hello".
	key := [4]byte{0x37, 0xfa, 0x21, 0x3d}
	masked := []byte{0x7f, 0x9f, 0x4d, 0x51, 0x58}
	payload := slices.Clone(masked)
	websocket.ApplyMask(payload, key)
	if string(payload) != "Hello" {
		t.Fatalf("unmasked = %q, want %q (RFC 6455 §5.7)", payload, "Hello")
	}
	websocket.ApplyMask(payload, key)
	if !bytes.Equal(payload, masked) {
		t.Fatalf("re-masked = % x, want % x — the transform must be its own inverse", payload, masked)
	}
}

// TestFrameHeaderLenCountsWhatFollowsTheFirstTwoBytes pins the arithmetic a
// reader depends on to avoid consuming payload it has not yet bounded.
func TestFrameHeaderLenCountsWhatFollowsTheFirstTwoBytes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		head []byte
		want int
	}
	tests := []tc{
		{"too short to say anything", []byte{0x81}, 0},
		{"7-bit length, unmasked", []byte{0x81, 0x05}, 2},
		{"7-bit length, masked", []byte{0x81, 0x85}, 6},
		{"16-bit length, unmasked", []byte{0x82, 0x7E}, 4},
		{"16-bit length, masked", []byte{0x82, 0xFE}, 8},
		{"64-bit length, unmasked", []byte{0x82, 0x7F}, 10},
		{"64-bit length, masked", []byte{0x82, 0xFF}, 14},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := websocket.FrameHeaderLen(c.head); got != c.want {
				t.Fatalf("FrameHeaderLen(% x) = %d, want %d", c.head, got, c.want)
			}
		})
	}
	//: the widest header the format can produce must still fit the constant a
	//: reader sizes its scratch from.
	if websocket.MaxHeaderLen != 14 {
		t.Fatalf("MaxHeaderLen = %d, want 14 (2 + 8 + 4)", websocket.MaxHeaderLen)
	}
}

// TestParseFrameHeaderRefusesWhatTheRFCForbids is the adversarial table: each
// case is a header a hostile or broken peer can produce, and each must fail the
// connection rather than be tolerated.
func TestParseFrameHeaderRefusesWhatTheRFCForbids(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		header []byte
		why    string
	}
	tests := []tc{
		{
			name: "RSV1 set with no extension negotiated",
			//: this is exactly what a permessage-deflate frame looks like.
			header: []byte{0xC1, 0x80, 0, 0, 0, 0},
			why:    "§5.2 — RSV bits must be zero unless an extension defines them",
		},
		{
			name:   "RSV2 set",
			header: []byte{0xA1, 0x80, 0, 0, 0, 0},
			why:    "§5.2",
		},
		{
			name:   "RSV3 set",
			header: []byte{0x91, 0x80, 0, 0, 0, 0},
			why:    "§5.2",
		},
		{
			name:   "reserved data opcode 0x3",
			header: []byte{0x83, 0x80, 0, 0, 0, 0},
			why:    "§5.2 — opcodes 0x3-0x7 are reserved",
		},
		{
			name:   "reserved control opcode 0xB",
			header: []byte{0x8B, 0x80, 0, 0, 0, 0},
			why:    "§5.2 — opcodes 0xB-0xF are reserved",
		},
		{
			name: "fragmented close frame",
			//: FIN clear on a control opcode.
			header: []byte{0x08, 0x80, 0, 0, 0, 0},
			why:    "§5.5 — control frames must not be fragmented",
		},
		{
			name:   "fragmented ping",
			header: []byte{0x09, 0x80, 0, 0, 0, 0},
			why:    "§5.5",
		},
		{
			name: "control frame carrying 126 bytes",
			//: the 16-bit length form on a ping.
			header: []byte{0x89, 0xFE, 0x00, 0x7E, 0, 0, 0, 0},
			why:    "§5.5 — a control payload must not exceed 125 bytes",
		},
		{
			name: "16-bit length spelling a 7-bit value",
			//: §5.2's own counter-example: 124 encoded as 126, 0, 124.
			header: []byte{0x81, 0xFE, 0x00, 0x7C, 0, 0, 0, 0},
			why:    "§5.2 — the minimal number of bytes MUST be used",
		},
		{
			name:   "64-bit length spelling a 16-bit value",
			header: []byte{0x82, 0xFF, 0, 0, 0, 0, 0, 0, 0x01, 0x00, 0, 0, 0, 0},
			why:    "§5.2 — the minimal number of bytes MUST be used",
		},
		{
			name:   "64-bit length with the most significant bit set",
			header: []byte{0x82, 0xFF, 0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			why:    "§5.2 — the most significant bit MUST be 0",
		},
		{
			name:   "a header that is not all there",
			header: []byte{0x81},
			why:    "a partial header cannot be parsed",
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := websocket.ParseFrameHeader(c.header)
			if err == nil {
				t.Fatalf("ParseFrameHeader(% x) = nil, want a refusal (%s)", c.header, c.why)
			}
			if !errs.HasCode(err, corenet.CodeWSProtocolViolation) {
				t.Fatalf("code = %v, want WS_PROTOCOL_VIOLATION (%s)", wsCodeOf(err), c.why)
			}
		})
	}
}

// TestParseFrameHeaderAcceptsWhatTheRFCAllows is the other half: the refusals
// above would also pass if the parser refused everything.
func TestParseFrameHeaderAcceptsWhatTheRFCAllows(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		header []byte
		want   websocket.FrameHeaderValue
	}
	tests := []tc{
		{
			name:   "masked 7-bit text",
			header: []byte{0x81, 0x85, 0x37, 0xfa, 0x21, 0x3d},
			want: websocket.FrameHeaderValue{
				Final: true, OpCode: corenet.WSText, Masked: true,
				MaskKey: [4]byte{0x37, 0xfa, 0x21, 0x3d}, Length: 5,
			},
		},
		{
			name:   "a 125-byte control frame is legal",
			header: []byte{0x89, 0xFD, 0, 0, 0, 0},
			want: websocket.FrameHeaderValue{
				Final: true, OpCode: corenet.WSPing, Masked: true, Length: 125,
			},
		},
		{
			name:   "a zero-length close frame is legal",
			header: []byte{0x88, 0x80, 0, 0, 0, 0},
			want: websocket.FrameHeaderValue{
				Final: true, OpCode: corenet.WSClose, Masked: true, Length: 0,
			},
		},
		{
			name:   "a non-final data frame opens a fragmented message",
			header: []byte{0x01, 0x83, 1, 2, 3, 4},
			want: websocket.FrameHeaderValue{
				Final: false, OpCode: corenet.WSText, Masked: true,
				MaskKey: [4]byte{1, 2, 3, 4}, Length: 3,
			},
		},
		{
			name:   "126 is the smallest legal 16-bit length",
			header: []byte{0x82, 0xFE, 0x00, 0x7E, 0, 0, 0, 0},
			want: websocket.FrameHeaderValue{
				Final: true, OpCode: corenet.WSBinary, Masked: true, Length: 126,
			},
		},
		{
			name:   "65536 is the smallest legal 64-bit length",
			header: []byte{0x82, 0xFF, 0, 0, 0, 0, 0, 0x01, 0x00, 0x00, 0, 0, 0, 0},
			want: websocket.FrameHeaderValue{
				Final: true, OpCode: corenet.WSBinary, Masked: true, Length: 65536,
			},
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := websocket.ParseFrameHeader(c.header)
			if err != nil {
				t.Fatalf("ParseFrameHeader(% x) = %v, want it accepted", c.header, err)
			}
			if got != c.want {
				t.Fatalf("header = %+v\nwant     %+v", got, c.want)
			}
		})
	}
}

// TestValidateFromClientFailsAnUnmaskedFrame pins §5.1's security requirement.
//
// It is a separate assertion from the parse table because it is the one rule
// whose answer depends on which side sent the frame — and the one whose
// violation is a security event rather than a formatting mistake.
func TestValidateFromClientFailsAnUnmaskedFrame(t *testing.T) {
	t.Parallel()
	//: §5.7's own unmasked text frame, which is perfectly legal FROM A SERVER
	//: and must be refused coming from a client.
	header, err := websocket.ParseFrameHeader([]byte{0x81, 0x05})
	if err != nil {
		t.Fatalf("ParseFrameHeader: %v", err)
	}
	if verr := header.ValidateFromClient(); verr == nil {
		t.Fatalf("an unmasked client frame was accepted; RFC 6455 §5.1 requires the connection to fail")
	} else if !errs.HasCode(verr, corenet.CodeWSProtocolViolation) {
		t.Fatalf("code = %v, want WS_PROTOCOL_VIOLATION", wsCodeOf(verr))
	}
	masked, merr := websocket.ParseFrameHeader([]byte{0x81, 0x85, 1, 2, 3, 4})
	if merr != nil {
		t.Fatalf("ParseFrameHeader: %v", merr)
	}
	if verr := masked.ValidateFromClient(); verr != nil {
		t.Fatalf("a masked client frame was refused: %v", verr)
	}
}

// TestAppendClosePayloadRefusesWhatMustNotTravel pins the sending half of
// §7.4, including the control-frame ceiling on the reason.
func TestAppendClosePayloadRefusesWhatMustNotTravel(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		code   corenet.WSCloseCode
		reason string
		ok     bool
	}
	tests := []tc{
		{"a normal closure with no reason", corenet.WSCloseNormal, "", true},
		{"a normal closure with a reason", corenet.WSCloseNormal, "done", true},
		{"going away", corenet.WSCloseGoingAway, "server shutting down", true},
		{"no-status must not be sent", corenet.WSCloseNoStatus, "", false},
		{"abnormal must not be sent", corenet.WSCloseAbnormal, "", false},
		{"the TLS code must not be sent", corenet.WSCloseTLSHandshake, "", false},
		{"the 1004 hole must not be sent", 1004, "", false},
		{"an unallocated code must not be sent", 2500, "", false},
		{"a reason that is not UTF-8", corenet.WSCloseNormal, string([]byte{0xFF, 0xFE}), false},
		{"a reason of exactly 123 bytes fits", corenet.WSCloseNormal, strings.Repeat("a", 123), true},
		{"a reason of 124 bytes does not", corenet.WSCloseNormal, strings.Repeat("a", 124), false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := websocket.AppendClosePayload(nil, c.code, c.reason)
			if c.ok {
				if err != nil {
					t.Fatalf("AppendClosePayload = %v, want it accepted", err)
				}
				if binary.BigEndian.Uint16(got) != uint16(c.code) {
					t.Fatalf("payload code = %d, want %d", binary.BigEndian.Uint16(got), c.code)
				}
				if string(got[2:]) != c.reason {
					t.Fatalf("payload reason = %q, want %q", got[2:], c.reason)
				}
				//: the whole payload must still fit a control frame.
				if len(got) > corenet.WSMaxControlPayload {
					t.Fatalf("payload is %d bytes, past the %d-byte control ceiling", len(got), corenet.WSMaxControlPayload)
				}
				return
			}
			if err == nil {
				t.Fatalf("AppendClosePayload(%d, %q) = nil, want a refusal", c.code, c.reason)
			}
			if got != nil {
				t.Fatalf("a refusal appended %d bytes; it must leave the buffer untouched", len(got))
			}
		})
	}
}

// TestParseClosePayloadReadsWhatTheRFCAllows pins the receiving half of
// §5.5.1, including the one-byte payload that is a protocol error rather than a
// truncated code to be salvaged.
func TestParseClosePayloadReadsWhatTheRFCAllows(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		payload    []byte
		wantCode   corenet.WSCloseCode
		wantReason string
		wantErr    errs.Code
	}
	tests := []tc{
		{
			name: "empty means no status", payload: nil,
			wantCode: corenet.WSCloseNoStatus,
		},
		{
			name: "a code alone", payload: []byte{0x03, 0xE8},
			wantCode: corenet.WSCloseNormal,
		},
		{
			name: "a code and a reason", payload: append([]byte{0x03, 0xE9}, "bye"...),
			wantCode: corenet.WSCloseGoingAway, wantReason: "bye",
		},
		{
			name: "one byte is not half a code", payload: []byte{0x03},
			wantErr: corenet.CodeWSProtocolViolation,
		},
		{
			name: "a code that must never travel", payload: []byte{0x03, 0xEE}, // 1006
			wantErr: corenet.CodeWSProtocolViolation,
		},
		{
			name: "the 1004 hole", payload: []byte{0x03, 0xEC},
			wantErr: corenet.CodeWSProtocolViolation,
		},
		{
			name: "a reason that is not UTF-8", payload: []byte{0x03, 0xE8, 0xFF, 0xFE},
			wantErr: corenet.CodeWSInvalidPayload,
		},
		{
			name: "a private-range code is accepted", payload: []byte{0x0F, 0xA0}, // 4000
			wantCode: 4000,
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			code, reason, err := websocket.ParseClosePayload(c.payload)
			if c.wantErr != 0 {
				if err == nil {
					t.Fatalf("ParseClosePayload(% x) = nil, want a refusal", c.payload)
				}
				if !errs.HasCode(err, c.wantErr) {
					t.Fatalf("code = %v, want %v", wsCodeOf(err), c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseClosePayload(% x) = %v", c.payload, err)
			}
			if code != c.wantCode || reason != c.wantReason {
				t.Fatalf("= (%d, %q), want (%d, %q)", code, reason, c.wantCode, c.wantReason)
			}
		})
	}
}

// TestValidateTextIsJudgedOnTheWholeMessage pins §8.1, and pins the reason
// validation happens after reassembly rather than per frame.
//
// The second half is the one that matters: a four-byte rune split across two
// fragments is VALID, and an implementation that validated each fragment would
// close a perfectly conformant connection with 1007.
func TestValidateTextIsJudgedOnTheWholeMessage(t *testing.T) {
	t.Parallel()
	//: U+1F600, four bytes, deliberately cut after the second.
	emoji := []byte{0xF0, 0x9F, 0x98, 0x80}
	head, tail := emoji[:2], emoji[2:]
	if err := websocket.ValidateText(emoji); err != nil {
		t.Fatalf("the whole rune was refused: %v", err)
	}
	if err := websocket.ValidateText(head); err == nil {
		t.Fatalf("half a rune passed validation; per-fragment checking would be wrong for the opposite reason")
	}
	//: reassembled, it is valid again — which is the whole point.
	if err := websocket.ValidateText(append(slices.Clone(head), tail...)); err != nil {
		t.Fatalf("the reassembled rune was refused: %v", err)
	}
	//: a genuinely invalid sequence stays invalid however it is assembled.
	if err := websocket.ValidateText([]byte{0xC0, 0x80}); err == nil {
		t.Fatalf("an overlong encoding passed validation")
	} else if !errs.HasCode(err, corenet.CodeWSInvalidPayload) {
		t.Fatalf("code = %v, want WS_INVALID_PAYLOAD", wsCodeOf(err))
	}
}

// wsCodeOf renders the dotted-quad code of an error for a failure message.
func wsCodeOf(err error) errs.Code {
	code, _ := errs.CodeOf(err)
	//: zero when the error carries none, which reads as "not one of ours".
	return code
}
