// Package net — the WebSocket application message and its UTF-8 rule
// (RFC 6455 §5.6 and §8.1).
package net

import (
	"unicode/utf8"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// WSMessageValue is one complete WebSocket application message, reassembled
// from however many frames carried it.
//
// A message is the unit an application reasons about; a frame is the unit the
// transport reasons about. Handing the application frames would make
// fragmentation — which is the sender's private choice of chunk size, not a
// semantic boundary — visible in every consumer.
type WSMessageValue struct {
	// Binary reports whether the payload is opaque bytes (opcode 0x2) rather
	// than UTF-8 text (opcode 0x1). The zero value is text, which is the
	// opcode a zero-valued frame would carry anyway.
	Binary bool `json:"binary"`
	// Data is the reassembled payload. For a text message it is valid UTF-8,
	// verified on both the receiving and the sending side.
	Data []byte `json:"data"`
}

// OpCode returns the frame opcode this message is carried under.
func (m WSMessageValue) OpCode() WSOpCode {
	//: the two data opcodes are the whole space a message can occupy.
	if m.Binary {
		//: opaque bytes.
		return WSBinary
	}
	//: UTF-8 text, the default.
	return WSText
}

// ValidateWSText reports whether a payload can travel as a text frame.
//
// Validation is performed on the REASSEMBLED message rather than per frame, and
// that is a correctness requirement rather than an optimisation: a multi-byte
// sequence may straddle a fragment boundary, so a per-frame check would reject
// perfectly valid messages whose only fault is where the sender chose to split
// them.
func ValidateWSText(b []byte) error {
	//: RFC 6455 §8.1 — an endpoint that finds a text payload is not UTF-8
	//: MUST fail the connection. Not sanitise it, not replace the bad bytes:
	//: a replacement character is a different message, silently substituted.
	if !utf8.Valid(b) {
		//: fail the connection with 1007.
		return errs.Wrap(WSInvalidPayload, errs.WrapParams{},
			errs.Int("length", len(b)),
			errs.String("why", "the text payload is not valid UTF-8"))
	}
	//: valid UTF-8.
	return nil
}
