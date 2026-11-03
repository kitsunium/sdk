package json

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corejson "github.com/kitsunium/sdk/internal/core/data/codec/json"
	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// v2Options are the options of the codec NewV2 returns: json/v2's defaults,
// and map members written in sorted order so the same value always encodes
// to the same bytes, as the registered codec's do.
var v2Options = jsonv2.Deterministic(true)

// v2Codec is the JSON codec under encoding/json/v2's own semantics (#893).
type v2Codec struct{}

// NewV2 returns a fresh JSON codec under encoding/json/v2's semantics, for a
// wire contract that starts with them: member names matched case-sensitively,
// a duplicate name or invalid UTF-8 refused, a nil slice or map written as []
// or {}, a byte array as base64, omitempty meaning an empty JSON value,
// omitzero honoured, a time.Duration refused without a format — and map
// members in sorted order. It is not registered: Format("json") keeps the v1
// semantics every existing document was written with, and this codec is
// reached only through the value returned. Its errors carry the codes of the
// registered codec.
func NewV2() codec.Codec {
	//: stateless; a fresh value per call keeps it out of the registry's way.
	return &v2Codec{}
}

// Name implements codec.Codec: the format is JSON, whatever the semantics.
func (*v2Codec) Name() string {
	//: the format's name.
	return "json"
}

// MIMETypes lists the JSON media types.
func (*v2Codec) MIMETypes() []string {
	//: the registered codec's, a fresh literal.
	return []string{"application/json", "text/json"}
}

// Extensions lists the JSON file extension.
func (*v2Codec) Extensions() []string {
	//: the registered codec's, a fresh literal.
	return []string{".json"}
}

// Marshal encodes v under json/v2's semantics.
func (*v2Codec) Marshal(v any) (encoded []byte, err error) {
	//: a channel, a function, an unsafe pointer: no wire format holds one (#1101).
	if verr := codec.CheckValue(v); verr != nil {
		return nil, verr
	}
	//: the v2 engine, its defaults.
	out, jerr := jsonv2.Marshal(v, v2Options)
	//: refused.
	if jerr != nil {
		return nil, marshalFailedV2(jerr, "service/data/codec/json.NewV2().Marshal: encoding/json/v2 returned an error")
	}
	//: the caller's bytes.
	return out, nil
}

// Unmarshal decodes data into v under json/v2's semantics.
func (*v2Codec) Unmarshal(data []byte, v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	//: no bytes is no document in this format (#1101).
	if len(data) == 0 {
		return codec.EmptyInput
	}
	//: the v2 engine, its defaults.
	jerr := jsonv2.Unmarshal(data, v, v2Options)
	//: success fast-path.
	if jerr == nil {
		return nil
	}
	//: refused.
	return unmarshalFailedV2(jerr, "service/data/codec/json.NewV2().Unmarshal: encoding/json/v2 returned an error")
}

// Append encodes v onto dst under json/v2's semantics, through a pooled
// scratch.AppendWriter; dst is returned as given on failure.
func (*v2Codec) Append(dst []byte, v any) (appended []byte, err error) {
	//: a pooled writer appending to dst.
	w := scratch.AcquireAppendWriter(dst)
	//: the value, straight onto dst.
	jerr := jsonv2.MarshalWrite(w, v, v2Options)
	//: the grown slice, read before the writer goes back to the pool.
	out := w.Bytes()
	scratch.ReleaseAppendWriter(w)
	//: refused: dst as it was given.
	if jerr != nil {
		return dst, marshalFailedV2(jerr, "service/data/codec/json.NewV2().Append: encoding/json/v2 returned an error")
	}
	//: the caller's prefix, then the value.
	return out, nil
}

// NewEncoder wraps w in a stream encoder writing one value per Encode, each
// followed by a newline, under json/v2's semantics.
func (*v2Codec) NewEncoder(w io.Writer) codec.Encoder {
	//: jsontext's encoder: one write and a newline per top-level value.
	return &v2Encoder{inner: jsontext.NewEncoder(w, v2Options)}
}

// NewDecoder wraps r in a stream decoder reading one value per Decode under
// json/v2's semantics. Decode returns io.EOF, unwrapped, once drained.
func (*v2Codec) NewDecoder(r io.Reader) codec.Decoder {
	//: jsontext's decoder.
	return &v2Decoder{inner: jsontext.NewDecoder(r, v2Options)}
}

// v2Encoder adapts a jsontext.Encoder to codec.Encoder.
type v2Encoder struct {
	// inner writes the stream.
	inner *jsontext.Encoder
}

// Encode writes v as the next top-level value.
func (e *v2Encoder) Encode(v any) error {
	//: the value, then the newline jsontext writes after each.
	jerr := jsonv2.MarshalEncode(e.inner, v)
	//: success fast-path.
	if jerr == nil {
		return nil
	}
	//: refused, or the writer failed.
	return marshalFailedV2(jerr, "service/data/codec/json.NewV2().Encoder.Encode: encoding/json/v2 returned an error")
}

// Close is a no-op: each value is flushed as it is written, and the encoder
// does not own the writer.
func (*v2Encoder) Close() error {
	//: nothing buffered.
	return nil
}

// v2Decoder adapts a jsontext.Decoder to codec.Decoder.
type v2Decoder struct {
	// inner reads the stream.
	inner *jsontext.Decoder
}

// Decode reads the next top-level value into v.
func (d *v2Decoder) Decode(v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	//: the next value.
	jerr := jsonv2.UnmarshalDecode(d.inner, v)
	//: success fast-path, and the end of the stream unwrapped.
	if jerr == nil || errors.Is(jerr, io.EOF) {
		return jerr
	}
	//: refused.
	return unmarshalFailedV2(jerr, "service/data/codec/json.NewV2().Decoder.Decode: encoding/json/v2 returned an error")
}

// More reports whether another value starts in the stream.
func (d *v2Decoder) More() bool {
	//: the next token's kind; none at the end or on an error, which Decode reports.
	return d.inner.PeekKind() != jsontext.KindInvalid
}

// marshalFailedV2 wraps a json/v2 encoding failure with the codec's code.
func marshalFailedV2(cause error, private string) error {
	//: the registered codec's code and texts.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    corejson.CodeJSONMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "JSON encoding failed",
		Private: private,
	})
}

// unmarshalFailedV2 wraps a json/v2 decoding failure with the codec's code,
// placed where json/v2 stopped.
func unmarshalFailedV2(cause error, private string) error {
	//: the registered codec's code and texts.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    corejson.CodeJSONUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "JSON decoding failed",
		Private: private,
	}, position(cause)...)
}
