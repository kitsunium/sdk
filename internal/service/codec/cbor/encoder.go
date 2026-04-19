// Package cbor: encoder.go adapts fxamacker's *Encoder to codec.Encoder.
package cbor

import (
	gocbor "github.com/fxamacker/cbor/v2"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// cborEncoder wraps *gocbor.Encoder so it satisfies codec.Encoder.
type cborEncoder struct {
	inner *gocbor.Encoder
}

// Encode serialises v through the wrapped encoder.
//
// Params:
//   - v: value to encode.
//
// Returns:
//   - error: MarshalFailed wrapping the library cause on failure.
func (e *cborEncoder) Encode(v any) (err error) {
	//: delegate and wrap on error.
	cerr := e.inner.Encode(v)
	//: success fast-path.
	if cerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library error.
	return errs.Wrap(cerr, errs.WrapParams{
		Code:    CodeCBORMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "CBOR encoding failed",
		Private: "service/codec/cbor.Encoder.Encode: fxamacker/cbor/v2 returned an error",
	})
}

// Close is a no-op because the fxamacker encoder does not own the writer.
//
// Returns:
//   - error: always nil.
func (*cborEncoder) Close() (err error) {
	//: fxamacker's encoder owns no writer-level state.
	return nil
}
