package json

import (
	stdjson "encoding/json"
	jsonv2 "encoding/json/v2"
	"io"
	"slices"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corejson "github.com/kitsunium/sdk/internal/core/data/codec/json"
	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Codec is the JSON singleton, registered with core/data/codec at package
// load. Binding the registration result to a named var is more idiomatic
// than `var _ = codec.Register(...)` and keeps us clear of init().
var Codec codec.Codec = codec.Register(&jsonCodec{})

// jsonCodec is the concrete Codec implementation for JSON.
type jsonCodec struct{}

// New returns the JSON codec singleton.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*jsonCodec) Name() string {
	//: canonical identifier.
	return "json"
}

// MIMETypes lists every MIME alias the codec recognises.
func (*jsonCodec) MIMETypes() []string {
	//: canonical type first so producers pick it by default.
	return []string{"application/json", "text/json"}
}

// Extensions lists every file extension the codec recognises.
func (*jsonCodec) Extensions() []string {
	//: canonical extension for JSON files.
	return []string{".json"}
}

// Marshal serialises v as JSON bytes. Pre-encoded inputs that already
// carry valid JSON bytes (`json.RawMessage`, `*json.RawMessage`) take
// a fast-path that bypasses the stdjson reflect + MarshalJSON
// round-trip — the caller did the work, we just return the bytes
// verbatim (after a defensive clone so the result is caller-owned).
func (*jsonCodec) Marshal(v any) (encoded []byte, err error) {
	//: a channel, a function, an unsafe pointer: no wire format holds one (#1101).
	if verr := codec.CheckValue(v); verr != nil {
		return nil, verr
	}
	//: pre-encoded fast-path — proxy/gateway callers hit this often.
	if out, ok := marshalRawMessage(v); ok {
		//: bytes are caller-owned now.
		return out, nil
	}
	//: json/v2 under the v1 options: the bytes encoding/json.Marshal writes —
	//: it is this very call in Go 1.27 — named here so the codec states the
	//: semantics it keeps (#893).
	out, jerr := jsonv2.Marshal(v, stdjson.DefaultOptionsV1())
	//: success fast-path.
	if jerr == nil {
		//: return the encoded bytes verbatim.
		return out, nil
	}
	//: wrap the stdlib error for reason-based matching.
	return nil, errs.Wrap(jerr, errs.WrapParams{
		Code:    corejson.CodeJSONMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "JSON encoding failed",
		Private: "service/data/codec/json.Marshal: encoding/json returned an error",
	})
}

// marshalRawMessage recognises pre-encoded json.RawMessage / *json.RawMessage
// values and returns their bytes verbatim (cloned so the caller cannot
// mutate the underlying buffer through our return). Returns ok=false
// when v isn't a recognised raw shape so the caller falls back to the
// stdjson reflect path.
func marshalRawMessage(v any) (encoded []byte, ok bool) {
	//: dispatch on the concrete RawMessage shapes only — anything else
	//: keeps the reflect-based path that handles arbitrary Go values.
	switch r := v.(type) {
	//: stdjson.RawMessage IS a []byte; clone so caller-owned.
	case stdjson.RawMessage:
		//: nil/empty RawMessage marshals as "null" — match stdjson semantics.
		if len(r) == 0 {
			//: explicit null wire bytes for empty raw message.
			return []byte("null"), true
		}
		//: caller-owned slice — defensive copy keeps storage independent.
		return slices.Clone([]byte(r)), true
	//: pointer form — dereference and recurse on the value.
	case *stdjson.RawMessage:
		//: nil pointer marshals as "null".
		if r == nil || len(*r) == 0 {
			//: explicit null wire bytes for nil/empty raw message.
			return []byte("null"), true
		}
		//: caller-owned slice.
		return slices.Clone([]byte(*r)), true
	//: no recognised raw shape — caller falls back.
	default:
		//: stdjson's reflect path handles the rest.
		return nil, false
	}
}

// Unmarshal parses data as JSON into v.
func (*jsonCodec) Unmarshal(data []byte, v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	//: no bytes is no document in this format (#1101).
	if len(data) == 0 {
		return codec.EmptyInput
	}
	//: json/v2 under the v1 options: what encoding/json.Unmarshal reads —
	//: case-insensitive member names, the last duplicate kept (#893).
	jerr := jsonv2.Unmarshal(data, v, stdjson.DefaultOptionsV1())
	//: success fast-path.
	if jerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the stdlib error.
	return errs.Wrap(jerr, errs.WrapParams{
		Code:    corejson.CodeJSONUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "JSON decoding failed",
		Private: "service/data/codec/json.Unmarshal: encoding/json returned an error",
	}, position(jerr)...)
}

// Append encodes v as JSON and appends the bytes to dst. Implements the
// optional codec.Appender interface so hot-path callers (logger encoder,
// batch sinks) can write into a recycled buffer.
//
// The value is encoded straight onto dst: json/v2's MarshalWrite under the
// v1 options — Marshal's bytes, with no trailing newline — writes through a
// pooled scratch.AppendWriter over dst, so the only copy is the encoder's own
// flush into the caller's buffer. On failure dst is returned as it was given.
func (*jsonCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: pre-encoded fast-path — same shape as Marshal, appended in place.
	if out, ok := appendRawMessage(dst, v); ok {
		//: the caller's prefix, then the raw bytes.
		return out, nil
	}
	//: a pooled writer appending to dst.
	w := scratch.AcquireAppendWriter(dst)
	//: the v1 options: the bytes encoding/json.Marshal writes.
	jerr := jsonv2.MarshalWrite(w, v, stdjson.DefaultOptionsV1())
	//: the grown slice, read before the writer goes back to the pool.
	out := w.Bytes()
	//: the writer forgets the slice; out stays the caller's.
	scratch.ReleaseAppendWriter(w)
	//: encoding failure — hand dst back as it was, surface the wrapped error.
	if jerr != nil {
		//: dst's own header: its length and its bytes are what the caller gave.
		return dst, errs.Wrap(jerr, errs.WrapParams{
			Code:    corejson.CodeJSONMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "JSON encoding failed",
			Private: "service/data/codec/json.Append: encoding/json returned an error",
		})
	}
	//: success — bytes are the caller's now.
	return out, nil
}

// appendRawMessage appends a pre-encoded json.RawMessage / *json.RawMessage
// to dst as marshalRawMessage returns it — "null" when empty — without the
// clone Marshal needs. ok=false when v is not a raw shape.
func appendRawMessage(dst []byte, v any) (appended []byte, ok bool) {
	//: only the two raw shapes.
	switch r := v.(type) {
	//: the value form.
	case stdjson.RawMessage:
		//: appended verbatim, null when empty.
		return appendRaw(dst, r), true
	//: the pointer form; nil reads as empty.
	case *stdjson.RawMessage:
		//: nil pointer marshals as "null".
		if r == nil {
			//: explicit null wire bytes.
			return append(dst, "null"...), true
		}
		//: appended verbatim, null when empty.
		return appendRaw(dst, *r), true
	//: anything else takes the encoder.
	default:
		//: the caller falls back.
		return dst, false
	}
}

// appendRaw appends raw to dst, or "null" when raw is empty.
func appendRaw(dst, raw []byte) []byte {
	//: an empty raw message marshals as null.
	if len(raw) == 0 {
		//: explicit null wire bytes.
		return append(dst, "null"...)
	}
	//: the caller vouches for the bytes, as on Marshal's path.
	return append(dst, raw...)
}

// NewEncoder wraps w in a streaming codec.Encoder.
func (*jsonCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: wrap the stdlib encoder to provide our Close contract.
	return &jsonEncoder{inner: stdjson.NewEncoder(w)}
}

// NewDecoder wraps r in a streaming codec.Decoder.
func (*jsonCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: wrap the stdlib decoder to expose More on our interface.
	return &jsonDecoder{inner: stdjson.NewDecoder(r)}
}
