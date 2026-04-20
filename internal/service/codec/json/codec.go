// Package json wraps encoding/json as a codec.Codec implementation
// registered under Format("json"). Blank-importing this package is enough
// to make JSON resolvable via the core/codec registry.
package json

import (
	stdjson "encoding/json"
	"io"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Codec is the JSON singleton, registered with core/codec at package load.
// Binding the registration result to a named var is more idiomatic than
// `var _ = codec.Register(...)` and keeps us clear of init() (KTN-FUNC-NOINIT).
var Codec codec.Codec = codec.Register(&jsonCodec{})

// jsonCodec is the concrete Codec implementation for JSON.
type jsonCodec struct{}

// New returns the JSON codec singleton.
//
// Returns:
//   - c: the shared stateless codec.
func New() (c codec.Codec) {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
//
// Returns:
//   - string: always "json".
func (*jsonCodec) Name() (name string) {
	//: canonical identifier.
	return "json"
}

// MIMETypes lists every MIME alias the codec recognises.
//
// Returns:
//   - []string: canonical MIME first.
func (*jsonCodec) MIMETypes() (mimes []string) {
	//: canonical type first so producers pick it by default.
	return []string{"application/json", "text/json"}
}

// Extensions lists every file extension the codec recognises.
//
// Returns:
//   - []string: canonical extension first.
func (*jsonCodec) Extensions() (exts []string) {
	//: canonical extension for JSON files.
	return []string{".json"}
}

// Marshal serialises v as JSON bytes.
//
// Params:
//   - v: any value encoding/json supports.
//
// Returns:
//   - []byte: the encoded JSON.
//   - error: MarshalFailed wrapping the stdlib cause on failure.
func (*jsonCodec) Marshal(v any) (data []byte, err error) {
	//: delegate to the stdlib for the actual encoding.
	out, jerr := stdjson.Marshal(v)
	//: success fast-path.
	if jerr == nil {
		//: return the encoded bytes verbatim.
		return out, nil
	}
	//: wrap the stdlib error for reason-based matching.
	return nil, errs.Wrap(jerr, errs.WrapParams{
		Code:    CodeJSONMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "JSON encoding failed",
		Private: "service/codec/json.Marshal: encoding/json returned an error",
	})
}

// Unmarshal parses data as JSON into v.
//
// Params:
//   - data: JSON bytes.
//   - v: pointer to the destination value.
//
// Returns:
//   - error: UnmarshalFailed wrapping the stdlib cause on failure.
func (*jsonCodec) Unmarshal(data []byte, v any) (err error) {
	//: delegate to the stdlib for the actual decoding.
	jerr := stdjson.Unmarshal(data, v)
	//: success fast-path.
	if jerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the stdlib error.
	return errs.Wrap(jerr, errs.WrapParams{
		Code:    CodeJSONUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "JSON decoding failed",
		Private: "service/codec/json.Unmarshal: encoding/json returned an error",
	})
}

// Append encodes v as JSON and appends the bytes to dst. Implements the
// optional codec.Appender interface so hot-path callers (logger encoder,
// batch sinks) can write into a recycled buffer.
//
// Params:
//   - dst: caller-supplied buffer; encoded bytes are appended onto it.
//   - v: any value encoding/json supports.
//
// Returns:
//   - []byte: the (possibly re-allocated) buffer with encoded JSON.
//   - error: MarshalFailed wrapping the stdlib cause on failure.
func (c *jsonCodec) Append(dst []byte, v any) (out []byte, err error) {
	//: delegate to Marshal so the wrap/error contract has a single source.
	encoded, merr := c.Marshal(v)
	//: surface any encoding failure without touching dst.
	if merr != nil {
		//: return the untouched buffer plus the wrapped error.
		return dst, merr
	}
	//: append the encoded bytes onto the caller's buffer.
	return append(dst, encoded...), nil
}

// NewEncoder wraps w in a streaming codec.Encoder.
//
// Params:
//   - w: destination writer.
//
// Returns:
//   - codec.Encoder: a streaming encoder bound to w.
func (*jsonCodec) NewEncoder(w io.Writer) (enc codec.Encoder) {
	//: wrap the stdlib encoder to provide our Close contract.
	return &jsonEncoder{inner: stdjson.NewEncoder(w)}
}

// NewDecoder wraps r in a streaming codec.Decoder.
//
// Params:
//   - r: source reader.
//
// Returns:
//   - codec.Decoder: a streaming decoder bound to r.
func (*jsonCodec) NewDecoder(r io.Reader) (dec codec.Decoder) {
	//: wrap the stdlib decoder to expose More on our interface.
	return &jsonDecoder{inner: stdjson.NewDecoder(r)}
}
