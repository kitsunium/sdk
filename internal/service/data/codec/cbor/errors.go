// Package cbor — declares the sentinel *errs.Error values for CBOR and the
// constructors every failure goes through. Each one carries the package's
// own code and reason, and a Private line that says what was refused — a
// type, a bound, an offset — and never a value read from the input.
package cbor

import "github.com/kitsunium/sdk/internal/kernel/errs"

// privatePrefix opens every Private line this package writes.
const privatePrefix string = "service/data/codec/cbor: "

// Sentinels, matched with errors.Is or errs.HasCode / errs.HasReason.
var (
	// MarshalFailed is the sentinel every encoding failure matches: a value
	// CBOR cannot carry, a string that is not UTF-8, a nesting deeper than
	// the decoder accepts, a failing writer, MarshalBinary or MarshalCBOR.
	MarshalFailed = errs.Define(CodeCBORMarshalFailed, "MARSHAL_FAILED",
		"CBOR encoding failed",
		"service/data/codec/cbor: a value could not be encoded as CBOR")

	// UnmarshalFailed is the sentinel every decoding failure matches: input
	// that is not one well-formed, valid data item, a bound exceeded, or an
	// item the target cannot hold.
	UnmarshalFailed = errs.Define(CodeCBORUnmarshalFailed, "UNMARSHAL_FAILED",
		"CBOR decoding failed",
		"service/data/codec/cbor: the input could not be decoded as CBOR")
)

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
		Code:    CodeCBORMarshalFailed,
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
		Code:    CodeCBORUnmarshalFailed,
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
		Code:    CodeCBORUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "CBOR decoding failed",
		Private: privatePrefix + detail,
	})
}
