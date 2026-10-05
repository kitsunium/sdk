package websocket

import (
	"encoding/binary"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// CloseCodeLen is the width of the status code that opens a Close frame's
// payload. A Close payload is therefore either empty or at least this long —
// one byte is not "a truncated code", it is a protocol error.
const CloseCodeLen int = 2

// AppendClosePayload appends a Close frame's payload — a two-byte status code
// followed by an optional UTF-8 reason — to dst.
//
// It refuses a code that must not appear on the wire and a reason that would
// push the control frame past its 125-byte ceiling. Both refusals happen before
// anything is appended.
func AppendClosePayload(dst []byte, code corenet.WSCloseCode, reason string) (wire []byte, err error) {
	//: 1005, 1006, 1015, 1004 and the unallocated ranges describe the ABSENCE
	//: of a close frame or nothing at all; sending one contradicts its own
	//: delivery.
	if !code.Sendable() {
		//: refuse rather than put a meaningless code on the wire.
		return dst, errs.Wrap(corenet.WSInvalidPayload, errs.WrapParams{},
			errs.Int("close_code", int(code)),
			errs.String("why", "the close code must not be sent on the wire"))
	}
	//: the reason travels as UTF-8 by definition (§5.5.1), so an invalid one
	//: would make the peer fail a connection we are politely closing.
	if verr := ValidateText([]byte(reason)); verr != nil {
		//: refuse rather than turn a clean close into a protocol error.
		return dst, verr
	}
	//: the code plus the reason must still fit a control frame.
	if CloseCodeLen+len(reason) > corenet.WSMaxControlPayload {
		//: refuse rather than truncate a reason into a different sentence.
		return dst, errs.Wrap(corenet.WSInvalidPayload, errs.WrapParams{},
			errs.Int("length", CloseCodeLen+len(reason)),
			errs.String("why", "the close payload exceeds the 125-byte control-frame ceiling"))
	}
	dst = binary.BigEndian.AppendUint16(dst, uint16(code))
	//: the reason is optional; an empty one appends nothing at all.
	return append(dst, reason...), nil
}

// ParseClosePayload reads a received Close frame's payload.
//
// An empty payload is legal and reports corenet.WSCloseNoStatus — the value
// that exists to describe exactly this, and that must never be sent. A
// ONE-byte payload is not a truncated code to be salvaged: it is a protocol
// error, and treating it as anything else would let a peer choose which half
// of the code this endpoint reads.
func ParseClosePayload(b []byte) (code corenet.WSCloseCode, reason string, err error) {
	//: the peer closed without saying why, which is its right.
	if len(b) == 0 {
		//: the value that means "no code was carried"; it is never sent.
		return corenet.WSCloseNoStatus, "", nil
	}
	//: half a status code is not a status code.
	if len(b) < CloseCodeLen {
		//: fail the connection rather than guess the missing byte.
		return 0, "", errs.Wrap(corenet.WSProtocolViolation, errs.WrapParams{},
			errs.Int("length", len(b)),
			errs.String("why", "a close payload carrying a status code must be at least two bytes"))
	}
	received := corenet.WSCloseCode(binary.BigEndian.Uint16(b))
	//: the same predicate that governs what we send governs what we accept —
	//: a peer sending 1006 is committing the error we refuse to commit.
	if !received.Sendable() {
		//: fail the connection.
		return 0, "", errs.Wrap(corenet.WSProtocolViolation, errs.WrapParams{},
			errs.Int("close_code", int(received)),
			errs.String("why", "the close code is reserved or must not be sent"))
	}
	text := b[CloseCodeLen:]
	//: §5.5.1 makes the reason UTF-8, and §8.1 makes invalid UTF-8 fatal.
	if verr := ValidateText(text); verr != nil {
		//: fail the connection with 1007, not 1002 — the frame's shape was
		//: fine, its payload was not.
		return 0, "", verr
	}
	//: the peer's code and its reason.
	return received, string(text), nil
}
