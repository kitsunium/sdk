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

// Package-level state: registers the JSON codec at package load via a var
// initialiser (not init() — KTN-FUNC-NOINIT).
var (
	_ = codec.Register(&jsonCodec{})
)

// jsonCodec is the concrete Codec implementation for JSON.
type jsonCodec struct{}

// New returns a JSON codec instance.
//
// Returns:
//   - codec.Codec: a fresh stateless codec.
func New() (c codec.Codec) {
	//: stateless — a fresh value is equivalent to a shared one.
	return &jsonCodec{}
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
		Code:    CodeMarshalFailed,
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
		Code:    CodeUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "JSON decoding failed",
		Private: "service/codec/json.Unmarshal: encoding/json returned an error",
	})
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
