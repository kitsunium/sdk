// Package toml wraps github.com/pelletier/go-toml/v2 as a codec.Codec
// implementation. The upstream library offers streaming Encoder/Decoder
// pairs, so this codec implements StreamingCodec as well.
package toml

import (
	"io"
	"slices"

	gotoml "github.com/pelletier/go-toml/v2"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Package-level state: registers the TOML codec at package load time.
var (
	_ = codec.Register(&tomlCodec{})

	//: MIME table hoisted to satisfy KTN-VAR-CONSTSLICE.
	mimeTypes = []string{"application/toml"}

	//: extension table hoisted for the same reason.
	extensions = []string{".toml"}
)

// tomlCodec is the concrete Codec implementation for TOML.
type tomlCodec struct{}

// New returns a TOML codec instance.
//
// Returns:
//   - codec.Codec: a fresh stateless codec.
func New() (c codec.Codec) {
	//: stateless singleton.
	return &tomlCodec{}
}

// Name implements codec.Codec.
//
// Returns:
//   - string: always "toml".
func (*tomlCodec) Name() (name string) {
	//: canonical identifier.
	return "toml"
}

// MIMETypes lists every MIME alias.
//
// Returns:
//   - []string: canonical MIME first.
func (*tomlCodec) MIMETypes() (mimes []string) {
	//: hand back the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
//
// Returns:
//   - []string: canonical extension first.
func (*tomlCodec) Extensions() (exts []string) {
	//: hand back the package-level slice.
	return slices.Clone(extensions)
}

// Marshal serialises v as TOML bytes.
//
// Params:
//   - v: value pelletier/go-toml/v2 supports (struct, map).
//
// Returns:
//   - []byte: the encoded TOML document.
//   - error: MarshalFailed wrapping the library cause on failure.
func (*tomlCodec) Marshal(v any) (data []byte, err error) {
	//: delegate to the library for the actual encoding.
	out, merr := gotoml.Marshal(v)
	//: success fast-path.
	if merr == nil {
		//: return the encoded bytes verbatim.
		return out, nil
	}
	//: wrap the library error for reason-based matching.
	return nil, errs.Wrap(merr, errs.WrapParams{
		Code:    CodeTOMLMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "TOML encoding failed",
		Private: "service/codec/toml.Marshal: pelletier/go-toml/v2 returned an error",
	})
}

// Unmarshal parses data as TOML into v.
//
// Params:
//   - data: TOML bytes.
//   - v: pointer to the destination value.
//
// Returns:
//   - error: UnmarshalFailed wrapping the library cause on failure.
func (*tomlCodec) Unmarshal(data []byte, v any) (err error) {
	//: delegate to the library for the actual decoding.
	uerr := gotoml.Unmarshal(data, v)
	//: success fast-path.
	if uerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library error.
	return errs.Wrap(uerr, errs.WrapParams{
		Code:    CodeTOMLUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "TOML decoding failed",
		Private: "service/codec/toml.Unmarshal: pelletier/go-toml/v2 returned an error",
	})
}

// NewEncoder wraps w in a streaming codec.Encoder.
//
// Params:
//   - w: destination writer.
//
// Returns:
//   - codec.Encoder: a streaming TOML encoder bound to w.
func (*tomlCodec) NewEncoder(w io.Writer) (enc codec.Encoder) {
	//: wrap the pelletier encoder.
	return &tomlEncoder{inner: gotoml.NewEncoder(w)}
}

// NewDecoder wraps r in a streaming codec.Decoder.
//
// Params:
//   - r: source reader.
//
// Returns:
//   - codec.Decoder: a streaming TOML decoder bound to r.
func (*tomlCodec) NewDecoder(r io.Reader) (dec codec.Decoder) {
	//: wrap the pelletier decoder.
	return &tomlDecoder{inner: gotoml.NewDecoder(r)}
}
