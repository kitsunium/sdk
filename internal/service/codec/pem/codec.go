// Package pem wraps encoding/pem as a codec.Codec implementation.
// PEM is block-structured: each wire message is a typed block ("CERTIFICATE",
// "PRIVATE KEY", etc.), so the codec operates on *pem.Block values.
package pem

import (
	"bytes"
	stdpem "encoding/pem"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Package-level state: registers the PEM codec at package load time and
// caches the MIME / extension tables to satisfy KTN-VAR-CONSTSLICE.
var (
	_ = codec.Register(&pemCodec{})

	//: no official IANA type exists; the de facto value is application/x-pem-file.
	mimeTypes = []string{"application/x-pem-file"}

	//: .pem is canonical; .crt/.key accepted as historical aliases.
	extensions = []string{".pem", ".crt", ".key"}
)

// pemCodec is the concrete Codec implementation for PEM.
type pemCodec struct{}

// Block re-exports encoding/pem.Block so consumers do not need to import
// the stdlib package directly.
type Block = stdpem.Block

// New returns a PEM codec instance.
//
// Returns:
//   - codec.Codec: a fresh stateless codec.
func New() (c codec.Codec) {
	//: stateless singleton.
	return &pemCodec{}
}

// Name implements codec.Codec.
//
// Returns:
//   - string: always "pem".
func (*pemCodec) Name() (name string) {
	//: canonical identifier.
	return "pem"
}

// MIMETypes lists every MIME alias.
//
// Returns:
//   - []string: canonical MIME first.
func (*pemCodec) MIMETypes() (mimes []string) {
	//: hand back the package-level slice.
	return mimeTypes
}

// Extensions lists every file extension.
//
// Returns:
//   - []string: canonical extension first.
func (*pemCodec) Extensions() (exts []string) {
	//: hand back the package-level slice.
	return extensions
}

// Marshal encodes a *pem.Block into PEM bytes.
//
// Params:
//   - v: must be *pem.Block (Block alias accepted via the type identity).
//
// Returns:
//   - []byte: the PEM-encoded bytes.
//   - error: ValueInvalid if v is not *pem.Block; MarshalFailed on writer failure.
func (*pemCodec) Marshal(v any) (data []byte, err error) {
	//: type-gate: PEM only accepts a typed block.
	block, ok := v.(*stdpem.Block)
	//: loud failure when the caller passed the wrong type.
	if !ok {
		//: shape-the-input rejection uses the VALUE_INVALID sentinel.
		return nil, errs.Wrap(nil, errs.WrapParams{
			Code:    CodeValueInvalid,
			Reason:  "VALUE_INVALID",
			Public:  "PEM codec requires a *pem.Block value",
			Private: "service/codec/pem.Marshal: argument is not *pem.Block",
		})
	}
	//: encode into a buffer so the caller gets []byte.
	var buf bytes.Buffer
	//: stdlib Encode writes directly; propagate any writer failure.
	if werr := stdpem.Encode(&buf, block); werr != nil {
		//: wrap the stdlib error.
		return nil, errs.Wrap(werr, errs.WrapParams{
			Code:    CodeMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "PEM encoding failed",
			Private: "service/codec/pem.Marshal: encoding/pem.Encode returned an error",
		})
	}
	//: hand back the buffered bytes.
	return buf.Bytes(), nil
}

// Unmarshal parses the first PEM block from data into v.
//
// Params:
//   - data: PEM bytes.
//   - v: pointer destination — must be **pem.Block.
//
// Returns:
//   - error: ValueInvalid if target is not **pem.Block; UnmarshalFailed if no block found.
func (*pemCodec) Unmarshal(data []byte, v any) (err error) {
	//: target must be **pem.Block so we can populate it.
	dst, ok := v.(**stdpem.Block)
	//: shape-the-target rejection.
	if !ok {
		//: loud failure when the caller passed the wrong type.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeValueInvalid,
			Reason:  "VALUE_INVALID",
			Public:  "PEM codec requires a **pem.Block target",
			Private: "service/codec/pem.Unmarshal: target is not **pem.Block",
		})
	}
	//: Decode returns the first block and any trailing bytes.
	block, _ := stdpem.Decode(data)
	//: no block = malformed input.
	if block == nil {
		//: loud failure — caller must be notified.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeUnmarshalFailed,
			Reason:  "UNMARSHAL_FAILED",
			Public:  "PEM decoding failed",
			Private: "service/codec/pem.Unmarshal: encoding/pem.Decode returned no block",
		})
	}
	//: publish the decoded block through the caller's pointer.
	*dst = block
	//: nothing to wrap.
	return nil
}
