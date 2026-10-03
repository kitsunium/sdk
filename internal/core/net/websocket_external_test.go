// Package net_test — the WebSocket protocol's values, checked against
// RFC 6455 itself rather than against this implementation's own idea of it:
// the GUID the handshake digests, the opcode space, the close-code registry
// and the message's opcode. The wire format those values travel in — the
// handshake digest, the frame codec, the close payload, the UTF-8 check — is
// internal/service/net/websocket's, and so are its tests (ADR 0160 §4).
package net_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

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
