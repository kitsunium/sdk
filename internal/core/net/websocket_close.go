// Package net — the WebSocket close code and the closing handshake payload
// (RFC 6455 §5.5.1 and §7.4).
package net

import (
	"encoding/binary"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// WSCloseCodeLen is the width of the status code that opens a Close frame's
// payload. A Close payload is therefore either empty or at least this long —
// one byte is not "a truncated code", it is a protocol error.
const WSCloseCodeLen int = 2

// The close codes RFC 6455 §7.4.1 defines. Three of them — WSCloseNoStatus,
// WSCloseAbnormal and WSCloseTLSHandshake — are values an application may
// OBSERVE but that MUST NOT appear on the wire: they describe the absence of a
// close frame, so sending one would be a statement contradicted by its own
// delivery.
const (
	// WSCloseNormal is a completed purpose, on either side.
	WSCloseNormal WSCloseCode = 1000
	// WSCloseGoingAway is a server shutting down or a browser navigating away.
	// It is what a drain sends.
	WSCloseGoingAway WSCloseCode = 1001
	// WSCloseProtocolError is a frame the RFC forbids.
	WSCloseProtocolError WSCloseCode = 1002
	// WSCloseUnsupportedData is a well-formed frame carrying data this endpoint
	// cannot accept — binary to a text-only peer, for instance.
	WSCloseUnsupportedData WSCloseCode = 1003
	// WSCloseNoStatus means the peer's Close frame carried no code. It is
	// reported to the application and never sent.
	WSCloseNoStatus WSCloseCode = 1005
	// WSCloseAbnormal means the connection died without a Close frame. It is
	// reported to the application and never sent.
	WSCloseAbnormal WSCloseCode = 1006
	// WSCloseInvalidPayload is a text frame that is not valid UTF-8, or a close
	// reason that is not.
	WSCloseInvalidPayload WSCloseCode = 1007
	// WSClosePolicyViolation is the generic refusal when no other code fits.
	WSClosePolicyViolation WSCloseCode = 1008
	// WSCloseTooLarge is a message beyond what this endpoint accepts.
	WSCloseTooLarge WSCloseCode = 1009
	// WSCloseExtensionRequired is a client giving up because the server did not
	// negotiate an extension it needs. A server never sends it.
	WSCloseExtensionRequired WSCloseCode = 1010
	// WSCloseInternalError is an unexpected condition on this side.
	WSCloseInternalError WSCloseCode = 1011
	// WSCloseTLSHandshake means the TLS handshake failed. It is reported to the
	// application and never sent — there is no connection to send it on.
	WSCloseTLSHandshake WSCloseCode = 1015
)

// The boundaries of the close-code ranges RFC 6455 §7.4.2 reserves.
const (
	// wsCloseProtocolLow is the first code the protocol itself may define.
	wsCloseProtocolLow WSCloseCode = 1000
	// wsCloseRegisteredHigh is the last code registered with IANA today
	// (1012 Service Restart, 1013 Try Again Later and 1014 Bad Gateway are
	// later registrations against the same range). Everything between here and
	// wsCloseLibraryLow is reserved-but-unallocated, so it is refused.
	wsCloseRegisteredHigh WSCloseCode = 1014
	// wsCloseUndefined is the one hole inside the assigned block: 1004 was
	// reserved and never given a meaning, so it must not be sent.
	wsCloseUndefined WSCloseCode = 1004
	// wsCloseLibraryLow opens the range libraries and frameworks register
	// first-come-first-served, which runs contiguously into the private range.
	wsCloseLibraryLow WSCloseCode = 3000
	// wsClosePrivateHigh closes the range applications may use without
	// registering anything.
	wsClosePrivateHigh WSCloseCode = 4999
)

// WSCloseCode is the status code a Close frame carries (RFC 6455 §7.4).
type WSCloseCode uint16

// Sendable reports whether the code may appear in a Close frame on the wire.
//
// It is deliberately a property of the CODE rather than a check inside the
// sender, because the same question is asked twice from opposite directions: a
// peer that sends 1006 is committing a protocol error, and so is this endpoint
// if it ever does. One predicate, both directions, no chance of the two
// drifting apart.
func (c WSCloseCode) Sendable() bool {
	//: the assigned block, minus the hole at 1004 and minus the three codes
	//: that describe the absence of a close frame (1005, 1006, 1015).
	if c >= wsCloseProtocolLow && c <= wsCloseRegisteredHigh {
		//: everything assigned is sendable except those three.
		return c != wsCloseUndefined && c != WSCloseNoStatus && c != WSCloseAbnormal
	}
	//: the library range (3000-3999) and the private one (4000-4999) are
	//: contiguous and both open. Everything else — below 1000, the gap above
	//: the registered block that swallows 1015, and past 4999 — is reserved
	//: and unallocated.
	return c >= wsCloseLibraryLow && c <= wsClosePrivateHigh
}

// Echoable returns the code to send back when answering this one.
//
// It exists because §5.5.1 says an endpoint SHOULD echo the peer's status code,
// and the one code a peer can leave us holding — WSCloseNoStatus, for a Close
// with no payload — is a code §7.4.1 forbids on the wire. Echoing blindly is
// therefore a protocol error waiting for the first well-behaved peer that
// closes without saying why.
func (c WSCloseCode) Echoable() WSCloseCode {
	//: the only unsendable value that can reach here is "the peer sent none",
	//: and a normal closure is the honest thing to answer: nothing went wrong.
	if !c.Sendable() {
		//: a completed purpose.
		return WSCloseNormal
	}
	//: §5.5.1 — the peer's own code is what it SHOULD receive.
	return c
}

// AppendWSClosePayload appends a Close frame's payload — a two-byte status code
// followed by an optional UTF-8 reason — to dst.
//
// It refuses a code that must not appear on the wire and a reason that would
// push the control frame past its 125-byte ceiling. Both refusals happen before
// anything is appended.
func AppendWSClosePayload(dst []byte, code WSCloseCode, reason string) (wire []byte, err error) {
	//: 1005, 1006, 1015, 1004 and the unallocated ranges describe the ABSENCE
	//: of a close frame or nothing at all; sending one contradicts its own
	//: delivery.
	if !code.Sendable() {
		//: refuse rather than put a meaningless code on the wire.
		return dst, errs.Wrap(WSInvalidPayload, errs.WrapParams{},
			errs.Int("close_code", int(code)),
			errs.String("why", "the close code must not be sent on the wire"))
	}
	//: the reason travels as UTF-8 by definition (§5.5.1), so an invalid one
	//: would make the peer fail a connection we are politely closing.
	if verr := ValidateWSText([]byte(reason)); verr != nil {
		//: refuse rather than turn a clean close into a protocol error.
		return dst, verr
	}
	//: the code plus the reason must still fit a control frame.
	if WSCloseCodeLen+len(reason) > WSMaxControlPayload {
		//: refuse rather than truncate a reason into a different sentence.
		return dst, errs.Wrap(WSInvalidPayload, errs.WrapParams{},
			errs.Int("length", WSCloseCodeLen+len(reason)),
			errs.String("why", "the close payload exceeds the 125-byte control-frame ceiling"))
	}
	dst = binary.BigEndian.AppendUint16(dst, uint16(code))
	//: the reason is optional; an empty one appends nothing at all.
	return append(dst, reason...), nil
}

// ParseWSClosePayload reads a received Close frame's payload.
//
// An empty payload is legal and reports [WSCloseNoStatus] — the value that
// exists to describe exactly this, and that must never be sent. A ONE-byte
// payload is not a truncated code to be salvaged: it is a protocol error, and
// treating it as anything else would let a peer choose which half of the code
// this endpoint reads.
func ParseWSClosePayload(b []byte) (code WSCloseCode, reason string, err error) {
	//: the peer closed without saying why, which is its right.
	if len(b) == 0 {
		//: the value that means "no code was carried"; it is never sent.
		return WSCloseNoStatus, "", nil
	}
	//: half a status code is not a status code.
	if len(b) < WSCloseCodeLen {
		//: fail the connection rather than guess the missing byte.
		return 0, "", errs.Wrap(WSProtocolViolation, errs.WrapParams{},
			errs.Int("length", len(b)),
			errs.String("why", "a close payload carrying a status code must be at least two bytes"))
	}
	received := WSCloseCode(binary.BigEndian.Uint16(b))
	//: the same predicate that governs what we send governs what we accept —
	//: a peer sending 1006 is committing the error we refuse to commit.
	if !received.Sendable() {
		//: fail the connection.
		return 0, "", errs.Wrap(WSProtocolViolation, errs.WrapParams{},
			errs.Int("close_code", int(received)),
			errs.String("why", "the close code is reserved or must not be sent"))
	}
	text := b[WSCloseCodeLen:]
	//: §5.5.1 makes the reason UTF-8, and §8.1 makes invalid UTF-8 fatal.
	if verr := ValidateWSText(text); verr != nil {
		//: fail the connection with 1007, not 1002 — the frame's shape was
		//: fine, its payload was not.
		return 0, "", verr
	}
	//: the peer's code and its reason.
	return received, string(text), nil
}
