// Package net_test — the WebSocket wire format, checked against RFC 6455
// itself rather than against this implementation's own idea of it.
//
// Every case below cites the section it comes from. Where the RFC prints a
// worked example — §1.3's handshake, §5.7's frames — the example is the
// expectation verbatim, because an implementation that agrees with itself
// proves nothing.
package net_test

import (
	"bytes"
	"encoding/binary"
	"slices"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestWSAcceptKeyMatchesTheWorkedExample pins §1.3's own numbers.
//
// The whole point of the accept value is that it cannot be produced by echoing
// the request, so the only honest test is one written from the RFC's example
// and not from this code.
func TestWSAcceptKeyMatchesTheWorkedExample(t *testing.T) {
	t.Parallel()
	//: RFC 6455 §1.3 — key "dGhlIHNhbXBsZSBub25jZQ==" yields this accept.
	const key = "dGhlIHNhbXBsZSBub25jZQ=="
	const want = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if got := corenet.WSAcceptKey(key); got != want {
		t.Fatalf("WSAcceptKey(%q) = %q, want %q (RFC 6455 §1.3)", key, got, want)
	}
}

// TestWSGUIDIsTheRFCConstant pins the one string the accept value is built on.
// A typo here would produce an implementation that only ever talks to itself.
func TestWSGUIDIsTheRFCConstant(t *testing.T) {
	t.Parallel()
	//: RFC 6455 §1.3.
	const want = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	if corenet.WSGUID != want {
		t.Fatalf("WSGUID = %q, want %q", corenet.WSGUID, want)
	}
}

// TestValidateWSKeyRefusesWhatIsNotANonce covers §4.1's requirement that the
// key be base64 of exactly sixteen bytes.
func TestValidateWSKeyRefusesWhatIsNotANonce(t *testing.T) {
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
			err := corenet.ValidateWSKey(c.key)
			if c.ok && err != nil {
				t.Fatalf("ValidateWSKey(%q) = %v, want nil", c.key, err)
			}
			if !c.ok {
				if err == nil {
					t.Fatalf("ValidateWSKey(%q) = nil, want a refusal", c.key)
				}
				if !errs.HasCode(err, corenet.CodeWSHandshakeFailed) {
					t.Fatalf("ValidateWSKey(%q) code = %v, want WS_HANDSHAKE_FAILED", c.key, wsCodeOf(err))
				}
			}
		})
	}
}

// TestAppendWSFrameMatchesTheWorkedExamples pins §5.7's frames byte for byte,
// including the two extended length forms.
//
// The length encodings are the part every implementation gets subtly wrong, so
// they are checked against the RFC's own hex rather than against a round trip
// through this package's parser — which would agree with any consistent bug.
func TestAppendWSFrameMatchesTheWorkedExamples(t *testing.T) {
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
			got, err := corenet.AppendWSFrame(nil, c.op, c.final, c.payload)
			if err != nil {
				t.Fatalf("AppendWSFrame: %v", err)
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

// TestAppendWSFrameLeavesTheBufferUntouchedOnRefusal pins the property a frame
// stream depends on: a refused frame must not leave half of itself on a wire
// the peer is already parsing.
func TestAppendWSFrameLeavesTheBufferUntouchedOnRefusal(t *testing.T) {
	t.Parallel()
	existing := []byte{0xDE, 0xAD}
	//: a control frame past the §5.5 ceiling.
	got, err := corenet.AppendWSFrame(existing, corenet.WSPing, true, bytes.Repeat([]byte{0}, 126))
	if err == nil {
		t.Fatalf("AppendWSFrame accepted a 126-byte ping; RFC 6455 §5.5 caps it at 125")
	}
	if !bytes.Equal(got, existing) {
		t.Fatalf("buffer = % x, want it untouched (% x)", got, existing)
	}
}

// TestApplyWSMaskDecodesTheWorkedExample pins §5.3's transform against §5.7's
// masked frame, and pins that it is its own inverse.
func TestApplyWSMaskDecodesTheWorkedExample(t *testing.T) {
	t.Parallel()
	//: §5.7 — "a single-frame masked text message" containing "Hello".
	key := [4]byte{0x37, 0xfa, 0x21, 0x3d}
	masked := []byte{0x7f, 0x9f, 0x4d, 0x51, 0x58}
	payload := slices.Clone(masked)
	corenet.ApplyWSMask(payload, key)
	if string(payload) != "Hello" {
		t.Fatalf("unmasked = %q, want %q (RFC 6455 §5.7)", payload, "Hello")
	}
	corenet.ApplyWSMask(payload, key)
	if !bytes.Equal(payload, masked) {
		t.Fatalf("re-masked = % x, want % x — the transform must be its own inverse", payload, masked)
	}
}

// TestWSFrameHeaderLenCountsWhatFollowsTheFirstTwoBytes pins the arithmetic a
// reader depends on to avoid consuming payload it has not yet bounded.
func TestWSFrameHeaderLenCountsWhatFollowsTheFirstTwoBytes(t *testing.T) {
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
			if got := corenet.WSFrameHeaderLen(c.head); got != c.want {
				t.Fatalf("WSFrameHeaderLen(% x) = %d, want %d", c.head, got, c.want)
			}
		})
	}
	//: the widest header the format can produce must still fit the constant a
	//: reader sizes its scratch from.
	if corenet.WSMaxHeaderLen != 14 {
		t.Fatalf("WSMaxHeaderLen = %d, want 14 (2 + 8 + 4)", corenet.WSMaxHeaderLen)
	}
}

// TestParseWSFrameHeaderRefusesWhatTheRFCForbids is the adversarial table: each
// case is a header a hostile or broken peer can produce, and each must fail the
// connection rather than be tolerated.
func TestParseWSFrameHeaderRefusesWhatTheRFCForbids(t *testing.T) {
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
			_, err := corenet.ParseWSFrameHeader(c.header)
			if err == nil {
				t.Fatalf("ParseWSFrameHeader(% x) = nil, want a refusal (%s)", c.header, c.why)
			}
			if !errs.HasCode(err, corenet.CodeWSProtocolViolation) {
				t.Fatalf("code = %v, want WS_PROTOCOL_VIOLATION (%s)", wsCodeOf(err), c.why)
			}
		})
	}
}

// TestParseWSFrameHeaderAcceptsWhatTheRFCAllows is the other half: the refusals
// above would also pass if the parser refused everything.
func TestParseWSFrameHeaderAcceptsWhatTheRFCAllows(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		header []byte
		want   corenet.WSFrameHeaderValue
	}
	tests := []tc{
		{
			name:   "masked 7-bit text",
			header: []byte{0x81, 0x85, 0x37, 0xfa, 0x21, 0x3d},
			want: corenet.WSFrameHeaderValue{
				Final: true, OpCode: corenet.WSText, Masked: true,
				MaskKey: [4]byte{0x37, 0xfa, 0x21, 0x3d}, Length: 5,
			},
		},
		{
			name:   "a 125-byte control frame is legal",
			header: []byte{0x89, 0xFD, 0, 0, 0, 0},
			want: corenet.WSFrameHeaderValue{
				Final: true, OpCode: corenet.WSPing, Masked: true, Length: 125,
			},
		},
		{
			name:   "a zero-length close frame is legal",
			header: []byte{0x88, 0x80, 0, 0, 0, 0},
			want: corenet.WSFrameHeaderValue{
				Final: true, OpCode: corenet.WSClose, Masked: true, Length: 0,
			},
		},
		{
			name:   "a non-final data frame opens a fragmented message",
			header: []byte{0x01, 0x83, 1, 2, 3, 4},
			want: corenet.WSFrameHeaderValue{
				Final: false, OpCode: corenet.WSText, Masked: true,
				MaskKey: [4]byte{1, 2, 3, 4}, Length: 3,
			},
		},
		{
			name:   "126 is the smallest legal 16-bit length",
			header: []byte{0x82, 0xFE, 0x00, 0x7E, 0, 0, 0, 0},
			want: corenet.WSFrameHeaderValue{
				Final: true, OpCode: corenet.WSBinary, Masked: true, Length: 126,
			},
		},
		{
			name:   "65536 is the smallest legal 64-bit length",
			header: []byte{0x82, 0xFF, 0, 0, 0, 0, 0, 0x01, 0x00, 0x00, 0, 0, 0, 0},
			want: corenet.WSFrameHeaderValue{
				Final: true, OpCode: corenet.WSBinary, Masked: true, Length: 65536,
			},
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := corenet.ParseWSFrameHeader(c.header)
			if err != nil {
				t.Fatalf("ParseWSFrameHeader(% x) = %v, want it accepted", c.header, err)
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
	header, err := corenet.ParseWSFrameHeader([]byte{0x81, 0x05})
	if err != nil {
		t.Fatalf("ParseWSFrameHeader: %v", err)
	}
	if verr := header.ValidateFromClient(); verr == nil {
		t.Fatalf("an unmasked client frame was accepted; RFC 6455 §5.1 requires the connection to fail")
	} else if !errs.HasCode(verr, corenet.CodeWSProtocolViolation) {
		t.Fatalf("code = %v, want WS_PROTOCOL_VIOLATION", wsCodeOf(verr))
	}
	masked, merr := corenet.ParseWSFrameHeader([]byte{0x81, 0x85, 1, 2, 3, 4})
	if merr != nil {
		t.Fatalf("ParseWSFrameHeader: %v", merr)
	}
	if verr := masked.ValidateFromClient(); verr != nil {
		t.Fatalf("a masked client frame was refused: %v", verr)
	}
}

// TestWSCloseCodeSendableFollowsTheRegistry walks §7.4.2's ranges, including
// every boundary — which is where a range check is wrong when it is wrong.
func TestWSCloseCodeSendableFollowsTheRegistry(t *testing.T) {
	t.Parallel()
	type tc struct {
		code corenet.WSCloseCode
		want bool
		why  string
	}
	tests := []tc{
		{0, false, "below every range"},
		{999, false, "§7.4.2 — 0-999 are not used"},
		{1000, true, "normal closure"},
		{1001, true, "going away"},
		{1002, true, "protocol error"},
		{1003, true, "unsupported data"},
		{1004, false, "§7.4.1 — reserved, never given a meaning"},
		{1005, false, "§7.4.1 — MUST NOT be set in a Close frame"},
		{1006, false, "§7.4.1 — MUST NOT be set in a Close frame"},
		{1007, true, "invalid payload data"},
		{1008, true, "policy violation"},
		{1009, true, "message too big"},
		{1010, true, "mandatory extension"},
		{1011, true, "internal error"},
		{1014, true, "IANA-registered bad gateway"},
		{1015, false, "§7.4.1 — MUST NOT be set in a Close frame"},
		{1016, false, "reserved for the protocol and unallocated"},
		{2999, false, "reserved for the protocol and unallocated"},
		{3000, true, "§7.4.2 — the library range opens here"},
		{3999, true, "§7.4.2 — the library range ends here"},
		{4000, true, "§7.4.2 — the private range opens here"},
		{4999, true, "§7.4.2 — the private range ends here"},
		{5000, false, "past every range"},
	}
	for _, c := range tests {
		if got := c.code.Sendable(); got != c.want {
			t.Errorf("WSCloseCode(%d).Sendable() = %t, want %t (%s)", c.code, got, c.want, c.why)
		}
	}
}

// TestAppendWSClosePayloadRefusesWhatMustNotTravel pins the sending half of
// §7.4, including the control-frame ceiling on the reason.
func TestAppendWSClosePayloadRefusesWhatMustNotTravel(t *testing.T) {
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
			got, err := corenet.AppendWSClosePayload(nil, c.code, c.reason)
			if c.ok {
				if err != nil {
					t.Fatalf("AppendWSClosePayload = %v, want it accepted", err)
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
				t.Fatalf("AppendWSClosePayload(%d, %q) = nil, want a refusal", c.code, c.reason)
			}
			if got != nil {
				t.Fatalf("a refusal appended %d bytes; it must leave the buffer untouched", len(got))
			}
		})
	}
}

// TestParseWSClosePayloadReadsWhatTheRFCAllows pins the receiving half of
// §5.5.1, including the one-byte payload that is a protocol error rather than a
// truncated code to be salvaged.
func TestParseWSClosePayloadReadsWhatTheRFCAllows(t *testing.T) {
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
			code, reason, err := corenet.ParseWSClosePayload(c.payload)
			if c.wantErr != 0 {
				if err == nil {
					t.Fatalf("ParseWSClosePayload(% x) = nil, want a refusal", c.payload)
				}
				if !errs.HasCode(err, c.wantErr) {
					t.Fatalf("code = %v, want %v", wsCodeOf(err), c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseWSClosePayload(% x) = %v", c.payload, err)
			}
			if code != c.wantCode || reason != c.wantReason {
				t.Fatalf("= (%d, %q), want (%d, %q)", code, reason, c.wantCode, c.wantReason)
			}
		})
	}
}

// TestValidateWSTextIsJudgedOnTheWholeMessage pins §8.1, and pins the reason
// validation happens after reassembly rather than per frame.
//
// The second half is the one that matters: a four-byte rune split across two
// fragments is VALID, and an implementation that validated each fragment would
// close a perfectly conformant connection with 1007.
func TestValidateWSTextIsJudgedOnTheWholeMessage(t *testing.T) {
	t.Parallel()
	//: U+1F600, four bytes, deliberately cut after the second.
	emoji := []byte{0xF0, 0x9F, 0x98, 0x80}
	head, tail := emoji[:2], emoji[2:]
	if err := corenet.ValidateWSText(emoji); err != nil {
		t.Fatalf("the whole rune was refused: %v", err)
	}
	if err := corenet.ValidateWSText(head); err == nil {
		t.Fatalf("half a rune passed validation; per-fragment checking would be wrong for the opposite reason")
	}
	//: reassembled, it is valid again — which is the whole point.
	if err := corenet.ValidateWSText(append(slices.Clone(head), tail...)); err != nil {
		t.Fatalf("the reassembled rune was refused: %v", err)
	}
	//: a genuinely invalid sequence stays invalid however it is assembled.
	if err := corenet.ValidateWSText([]byte{0xC0, 0x80}); err == nil {
		t.Fatalf("an overlong encoding passed validation")
	} else if !errs.HasCode(err, corenet.CodeWSInvalidPayload) {
		t.Fatalf("code = %v, want WS_INVALID_PAYLOAD", wsCodeOf(err))
	}
}

// TestWSOpCodeClassification pins the split at 8 that lets an endpoint classify
// an opcode it does not recognise.
func TestWSOpCodeClassification(t *testing.T) {
	t.Parallel()
	type tc struct {
		op        corenet.WSOpCode
		control   bool
		defined   bool
		rendering string
	}
	tests := []tc{
		{corenet.WSContinuation, false, true, "continuation"},
		{corenet.WSText, false, true, "text"},
		{corenet.WSBinary, false, true, "binary"},
		{0x3, false, false, "reserved"},
		{0x7, false, false, "reserved"},
		{corenet.WSClose, true, true, "close"},
		{corenet.WSPing, true, true, "ping"},
		{corenet.WSPong, true, true, "pong"},
		{0xB, true, false, "reserved"},
		{0xF, true, false, "reserved"},
	}
	for _, c := range tests {
		if got := c.op.IsControl(); got != c.control {
			t.Errorf("WSOpCode(%#x).IsControl() = %t, want %t", c.op, got, c.control)
		}
		if got := c.op.Defined(); got != c.defined {
			t.Errorf("WSOpCode(%#x).Defined() = %t, want %t", c.op, got, c.defined)
		}
		if got := c.op.String(); got != c.rendering {
			t.Errorf("WSOpCode(%#x).String() = %q, want %q", c.op, got, c.rendering)
		}
	}
}

// TestEchoableNeverPutsAnUnsendableCodeOnTheWire pins §7.4.1's prohibition at
// the one place it is easy to violate by accident: echoing back whatever the
// peer sent.
//
// §5.5.1 says an endpoint SHOULD echo the peer's status code, and the one code
// a peer can leave an endpoint holding — 1005, for a Close with no payload — is
// a code §7.4.1 forbids on the wire. Echoing blindly is therefore a protocol
// error waiting for the first well-behaved peer that closes without saying why.
func TestEchoableNeverPutsAnUnsendableCodeOnTheWire(t *testing.T) {
	t.Parallel()
	//: the code recorded for a peer that sent none.
	if got := corenet.WSCloseNoStatus.Echoable(); got != corenet.WSCloseNormal {
		t.Fatalf("WSCloseNoStatus.Echoable() = %d, want 1000", got)
	}
	//: everything the RFC allows on the wire is echoed unchanged.
	for _, code := range []corenet.WSCloseCode{1000, 1001, 1008, 3000, 4999} {
		if got := code.Echoable(); got != code {
			t.Fatalf("WSCloseCode(%d).Echoable() = %d, want it unchanged", code, got)
		}
	}
	//: and the invariant that matters: whatever comes out is sendable.
	for code := range corenet.WSCloseCode(5100) {
		if !code.Echoable().Sendable() {
			t.Fatalf("WSCloseCode(%d).Echoable() = %d, which must not be sent", code, code.Echoable())
		}
	}
}

// wsCodeOf renders the dotted-quad code of an error for a failure message.
func wsCodeOf(err error) errs.Code {
	code, _ := errs.CodeOf(err)
	//: zero when the error carries none, which reads as "not one of ours".
	return code
}

// TestWSMessageValueOpCode pins that the zero value is a text message, which is
// what a caller writing Message{Data: …} means.
func TestWSMessageValueOpCode(t *testing.T) {
	t.Parallel()
	if got := (corenet.WSMessageValue{}).OpCode(); got != corenet.WSText {
		t.Fatalf("the zero message is opcode %v, want text", got)
	}
	if got := (corenet.WSMessageValue{Binary: true}).OpCode(); got != corenet.WSBinary {
		t.Fatalf("a binary message is opcode %v, want binary", got)
	}
}
