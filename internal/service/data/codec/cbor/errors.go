package cbor

import (
	corecbor "github.com/kitsunium/sdk/internal/core/data/codec/cbor"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// privatePrefix opens every Private line this package writes.
const privatePrefix string = "service/data/codec/cbor: "

// encodeFailure is the MARSHAL_FAILED error for a refusal the encoder
// reached itself; detail says what was refused.
func encodeFailure(detail string) error {
	//: no cause: the encoder is the origin.
	return encodeCause(nil, detail)
}

// encodeCause is the MARSHAL_FAILED error wrapping cause — a writer's, or a
// MarshalBinary's or MarshalCBOR's failure. An SDK error keeps its own code
// and reason: origin wins.
func encodeCause(cause error, detail string) error {
	//: one wrap site for every encoding failure.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    corecbor.CodeCBORMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "CBOR encoding failed",
		Private: privatePrefix + detail,
	})
}

// malformed is the UNMARSHAL_FAILED error for input refused at offset off:
// not well-formed, not valid, or past a bound.
func malformed(off int, detail string) error {
	//: the offset is the one field: where to look, never what was there.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    corecbor.CodeCBORUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "CBOR decoding failed",
		Private: privatePrefix + detail,
	}, errs.Int("offset", off))
}

// decodeCause is the UNMARSHAL_FAILED error wrapping cause — a reader's,
// or an UnmarshalBinary's or UnmarshalCBOR's failure — or, with a nil cause,
// the refusal of an item the target cannot hold. An SDK error keeps its own
// code and reason: origin wins.
func decodeCause(cause error, detail string) error {
	//: one wrap site for every decoding failure with a cause.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    corecbor.CodeCBORUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "CBOR decoding failed",
		Private: privatePrefix + detail,
	})
}
