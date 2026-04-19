// Package cbor wraps github.com/fxamacker/cbor/v2 as a codec.Codec
// implementation. fxamacker exposes an Encoder and Decoder that stream
// individual CBOR items, so this codec also implements StreamingCodec.
package cbor

import (
	"io"

	gocbor "github.com/fxamacker/cbor/v2"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Package-level state: registers the CBOR codec at package load time.
var (
	_ = codec.Register(&cborCodec{})

	//: MIME table hoisted to satisfy KTN-VAR-CONSTSLICE.
	mimeTypes = []string{"application/cbor"}

	//: extension table hoisted for the same reason.
	extensions = []string{".cbor"}
)

// cborCodec is the concrete Codec implementation for CBOR.
type cborCodec struct{}

// New returns a CBOR codec instance.
//
// Returns:
//   - codec.Codec: a fresh stateless codec.
func New() (c codec.Codec) {
	//: stateless singleton.
	return &cborCodec{}
}

// Name implements codec.Codec.
//
// Returns:
//   - string: always "cbor".
func (*cborCodec) Name() (name string) {
	//: canonical identifier.
	return "cbor"
}

// MIMETypes lists every MIME alias.
//
// Returns:
//   - []string: canonical MIME first.
func (*cborCodec) MIMETypes() (mimes []string) {
	//: hand back the package-level slice.
	return mimeTypes
}

// Extensions lists every file extension.
//
// Returns:
//   - []string: canonical extension first.
func (*cborCodec) Extensions() (exts []string) {
	//: hand back the package-level slice.
	return extensions
}

// Marshal serialises v as CBOR bytes.
//
// Params:
//   - v: value fxamacker/cbor/v2 supports.
//
// Returns:
//   - []byte: the encoded CBOR bytes.
//   - error: MarshalFailed wrapping the library cause on failure.
func (*cborCodec) Marshal(v any) (data []byte, err error) {
	//: delegate to the library for the actual encoding.
	out, merr := gocbor.Marshal(v)
	//: success fast-path.
	if merr == nil {
		//: return the encoded bytes verbatim.
		return out, nil
	}
	//: wrap the library error for reason-based matching.
	return nil, errs.Wrap(merr, errs.WrapParams{
		Code:    CodeCBORMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "CBOR encoding failed",
		Private: "service/codec/cbor.Marshal: fxamacker/cbor/v2 returned an error",
	})
}

// Unmarshal parses data as CBOR into v.
//
// Params:
//   - data: CBOR bytes.
//   - v: pointer to the destination value.
//
// Returns:
//   - error: UnmarshalFailed wrapping the library cause on failure.
func (*cborCodec) Unmarshal(data []byte, v any) (err error) {
	//: delegate to the library for the actual decoding.
	uerr := gocbor.Unmarshal(data, v)
	//: success fast-path.
	if uerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library error.
	return errs.Wrap(uerr, errs.WrapParams{
		Code:    CodeCBORUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "CBOR decoding failed",
		Private: "service/codec/cbor.Unmarshal: fxamacker/cbor/v2 returned an error",
	})
}

// NewEncoder wraps w in a streaming codec.Encoder.
//
// Params:
//   - w: destination writer.
//
// Returns:
//   - codec.Encoder: a streaming CBOR encoder bound to w.
func (*cborCodec) NewEncoder(w io.Writer) (enc codec.Encoder) {
	//: wrap the fxamacker encoder.
	return &cborEncoder{inner: gocbor.NewEncoder(w)}
}

// NewDecoder wraps r in a streaming codec.Decoder.
//
// Params:
//   - r: source reader.
//
// Returns:
//   - codec.Decoder: a streaming CBOR decoder bound to r.
func (*cborCodec) NewDecoder(r io.Reader) (dec codec.Decoder) {
	//: wrap the fxamacker decoder.
	return &cborDecoder{inner: gocbor.NewDecoder(r)}
}
