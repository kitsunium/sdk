// Package toml wraps github.com/pelletier/go-toml/v2 as a codec.Codec
// implementation. The upstream library offers streaming Encoder/Decoder
// pairs, so this codec implements StreamingCodec as well.
package toml

import (
	"io"
	"slices"

	gotoml "github.com/pelletier/go-toml/v2"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/core/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables.
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&tomlCodec{})

	//: MIME table hoisted.
	mimeTypes = []string{"application/toml"}

	//: extension table hoisted for the same reason.
	extensions = []string{".toml"}
)

// tomlCodec is the concrete Codec implementation for TOML.
type tomlCodec struct{}

// New returns a TOML codec instance.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*tomlCodec) Name() string {
	//: canonical identifier.
	return "toml"
}

// MIMETypes lists every MIME alias.
func (*tomlCodec) MIMETypes() []string {
	//: hand back the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
func (*tomlCodec) Extensions() []string {
	//: hand back the package-level slice.
	return slices.Clone(extensions)
}

// Marshal serialises v as TOML bytes. Routes through a pooled
// *bytes.Buffer + gotoml.NewEncoder so the per-call bytes.Buffer
// allocation gotoml.Marshal pays internally is amortised across calls.
func (*tomlCodec) Marshal(v any) (encoded []byte, err error) {
	//: rent an already-Reset buffer from the shared codec pool.
	buf := scratch.AcquireBuffer()
	//: pelletier Encoder has no Reset — fresh one per call.
	enc := gotoml.NewEncoder(buf)
	//: encode into the pooled buffer.
	if merr := enc.Encode(v); merr != nil {
		//: drop the buffer back to the pool if not oversized.
		scratch.ReleaseBuffer(buf)
		//: wrap the library error for reason-based matching.
		return nil, errs.Wrap(merr, errs.WrapParams{
			Code:    CodeTOMLMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "TOML encoding failed",
			Private: "service/codec/toml.Marshal: pelletier/go-toml/v2 returned an error",
		})
	}
	//: detach: clone the buffer's bytes so the returned slice does not
	//: alias the pooled buffer (next caller would overwrite it).
	out := slices.Clone(buf.Bytes())
	//: cap-discard release.
	scratch.ReleaseBuffer(buf)
	//: success — bytes are the caller's now.
	return out, nil
}

// Unmarshal parses data as TOML into v.
func (*tomlCodec) Unmarshal(data []byte, v any) error {
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

// Append encodes v as TOML and appends the bytes to dst. Implements the
// optional codec.Appender interface so hot-path callers can stream
// records into a recycled buffer. Encodes directly into the pooled
// *bytes.Buffer + appends onto dst — saves the slices.Clone the
// Marshal-delegation shape paid.
func (*tomlCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: rent an already-Reset buffer from the shared codec pool.
	buf := scratch.AcquireBuffer()
	//: pelletier's Encoder has no Reset(w) — fresh one per call.
	enc := gotoml.NewEncoder(buf)
	//: encode into the pooled buffer.
	if merr := enc.Encode(v); merr != nil {
		//: cap-discard release; dst stays pristine, error surfaces.
		scratch.ReleaseBuffer(buf)
		//: wrap the library error for reason-based matching.
		return dst, errs.Wrap(merr, errs.WrapParams{
			Code:    CodeTOMLMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "TOML encoding failed",
			Private: "service/codec/toml.Append: pelletier/go-toml/v2 returned an error",
		})
	}
	//: append the encoded bytes onto the caller's buffer (1 copy total).
	dst = append(dst, buf.Bytes()...)
	//: cap-discard release.
	scratch.ReleaseBuffer(buf)
	//: success — bytes are the caller's now.
	return dst, nil
}

// NewEncoder wraps w in a streaming codec.Encoder.
func (*tomlCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: wrap the pelletier encoder.
	return &tomlEncoder{inner: gotoml.NewEncoder(w)}
}

// NewDecoder wraps r in a streaming codec.Decoder.
func (*tomlCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: wrap the pelletier decoder.
	return &tomlDecoder{inner: gotoml.NewDecoder(r)}
}
