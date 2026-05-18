// Package cbor: decoder.go adapts fxamacker's *Decoder to codec.Decoder.
package cbor

import (
	"errors"
	"io"

	gocbor "github.com/fxamacker/cbor/v2"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// cborDecoder wraps *gocbor.Decoder so it satisfies codec.Decoder.
type cborDecoder struct {
	inner *gocbor.Decoder
	//: sticky EOF flag so More() returns false once the stream drains.
	done bool
}

// Decode reads the next CBOR item into v.
//
// Params:
//   - v: pointer destination.
//
// Returns:
//   - error: UnmarshalFailed on failure, io.EOF when the stream is drained.
func (d *cborDecoder) Decode(v any) error {
	//: delegate to the library.
	derr := d.inner.Decode(v)
	//: EOF latch toggles the done flag.
	if errors.Is(derr, io.EOF) {
		//: remember we reached the stream end.
		d.done = true
		//: return io.EOF untouched.
		return io.EOF
	}
	//: success fast-path.
	if derr == nil {
		//: nothing to wrap.
		return nil
	}
	//: mark drained so More() stops a dec.More()/Decode() loop on error.
	d.done = true
	//: wrap the library error for reason-based matching.
	return errs.Wrap(derr, errs.WrapParams{
		Code:    CodeCBORUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "CBOR decoding failed",
		Private: "service/codec/cbor.Decoder.Decode: fxamacker/cbor/v2 returned an error",
	})
}

// More reports whether additional items are still available.
//
// Returns:
//   - bool: false once Decode has returned io.EOF.
func (d *cborDecoder) More() bool {
	//: reflect the sticky EOF latch.
	return !d.done
}
