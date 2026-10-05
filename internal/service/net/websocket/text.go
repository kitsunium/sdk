package websocket

import (
	"unicode/utf8"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ValidateText reports whether a payload can travel as a text frame.
//
// Validation is performed on the REASSEMBLED message rather than per frame, and
// that is a correctness requirement rather than an optimisation: a multi-byte
// sequence may straddle a fragment boundary, so a per-frame check would reject
// perfectly valid messages whose only fault is where the sender chose to split
// them.
func ValidateText(b []byte) error {
	//: RFC 6455 §8.1 — an endpoint that finds a text payload is not UTF-8
	//: MUST fail the connection. Not sanitise it, not replace the bad bytes:
	//: a replacement character is a different message, silently substituted.
	if !utf8.Valid(b) {
		//: fail the connection with 1007.
		return errs.Wrap(corenet.WSInvalidPayload, errs.WrapParams{},
			errs.Int("length", len(b)),
			errs.String("why", "the text payload is not valid UTF-8"))
	}
	//: valid UTF-8.
	return nil
}
