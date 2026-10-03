// Package cbor — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
package cbor

import "github.com/kitsunium/sdk/internal/kernel/errs"

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
