// Package pem wraps encoding/pem as a codec.Codec implementation.
// PEM is block-structured: each wire message is a typed block ("CERTIFICATE",
// "PRIVATE KEY", etc.), so the codec operates on *pem.Block values.
package pem

import (
	"bytes"
	stdpem "encoding/pem"
	"slices"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables (hoisted).
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&pemCodec{})

	//: no official IANA type exists; the de facto value is application/x-pem-file.
	mimeTypes = []string{"application/x-pem-file"}

	//: .pem is the canonical extension; .crt and .key are historical aliases.
	extensions = []string{".pem", ".crt", ".key"}
)

// pemCodec is the concrete Codec implementation for PEM.
type pemCodec struct{}

// Block re-exports encoding/pem.Block so consumers do not need to import
// the stdlib package directly.
type Block = stdpem.Block

// New returns a PEM codec instance.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*pemCodec) Name() string {
	//: canonical identifier.
	return "pem"
}

// MIMETypes lists every MIME alias.
func (*pemCodec) MIMETypes() []string {
	//: hand back the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
func (*pemCodec) Extensions() []string {
	//: hand back the package-level slice.
	return slices.Clone(extensions)
}

// Marshal encodes a *pem.Block into PEM bytes.
func (*pemCodec) Marshal(v any) (encoded []byte, err error) {
	//: type-gate: PEM only accepts a typed block.
	block, ok := v.(*stdpem.Block)
	//: loud failure when the caller passed the wrong type OR a nil block.
	if !ok || block == nil {
		//: shape-the-input rejection uses the VALUE_INVALID sentinel.
		return nil, errs.Wrap(nil, errs.WrapParams{
			Code:    CodePEMValueInvalid,
			Reason:  "VALUE_INVALID",
			Public:  "PEM codec requires a non-nil *pem.Block value",
			Private: "service/codec/pem.Marshal: argument is not a non-nil *pem.Block",
		})
	}
	//: encode into a buffer so the caller gets []byte.
	var buf bytes.Buffer
	//: stdlib Encode writes directly; propagate any writer failure.
	if werr := stdpem.Encode(&buf, block); werr != nil {
		//: wrap the stdlib error.
		return nil, errs.Wrap(werr, errs.WrapParams{
			Code:    CodePEMMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "PEM encoding failed",
			Private: "service/codec/pem.Marshal: encoding/pem.Encode returned an error",
		})
	}
	//: hand back the buffered bytes.
	return buf.Bytes(), nil
}

// Append encodes a *pem.Block as PEM bytes and appends them to dst.
// Implements the optional codec.Appender interface so hot-path callers
// can stitch PEM blocks into a larger framed buffer (multi-block cert
// chain construction) without an intermediate allocation per block.
func (c *pemCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: delegate to Marshal so the type-gate + wrap/error contract has
	//: a single source.
	encoded, merr := c.Marshal(v)
	//: surface any encoding failure without touching dst.
	if merr != nil {
		//: return the untouched buffer plus the wrapped error.
		return dst, merr
	}
	//: append the encoded bytes onto the caller's buffer.
	return append(dst, encoded...), nil
}

// Unmarshal parses the first PEM block from data into v.
func (*pemCodec) Unmarshal(data []byte, v any) error {
	//: target must be **pem.Block so we can populate it.
	dst, ok := v.(**stdpem.Block)
	//: shape-the-target rejection — wrong type or nil outer pointer panics later.
	if !ok || dst == nil {
		//: loud failure when the caller passed the wrong type.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodePEMValueInvalid,
			Reason:  "VALUE_INVALID",
			Public:  "PEM codec requires a non-nil **pem.Block target",
			Private: "service/codec/pem.Unmarshal: target is not a non-nil **pem.Block",
		})
	}
	//: Decode returns the first block and any trailing bytes.
	block, _ := stdpem.Decode(data)
	//: no block = malformed input.
	if block == nil {
		//: loud failure — caller must be notified.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodePEMUnmarshalFailed,
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
