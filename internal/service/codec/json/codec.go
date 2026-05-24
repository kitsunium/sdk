// Package json wraps encoding/json as a codec.Codec implementation
// registered under Format("json"). Blank-importing this package is enough
// to make JSON resolvable via the core/codec registry.
package json

import (
	stdjson "encoding/json"
	"io"
	"slices"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/core/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Codec is the JSON singleton, registered with core/codec at package
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
	//: pre-encoded fast-path — proxy/gateway callers hit this often.
	if out, ok := marshalRawMessage(v); ok {
		//: bytes are caller-owned now.
		return out, nil
	}
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
// Uses a *bytes.Buffer pool + json.NewEncoder(buf).Encode(v) — replaces
// the previous Marshal-then-append shape which paid a double copy
// (stdjson.Marshal allocs+copies into a fresh []byte, then append copies
// again into dst). The pool path encodes once into a reusable buffer
// then copies once into dst — one alloc, one copy in steady state.
func (*jsonCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: pre-encoded fast-path — same shape as Marshal, no scratch buffer
	//: needed because the bytes already exist.
	if out, ok := marshalRawMessage(v); ok {
		//: append the pre-encoded bytes directly onto the caller's dst.
		return append(dst, out...), nil
	}
	//: rent an already-Reset buffer from the shared codec pool.
	buf := scratch.AcquireBuffer()
	//: stdjson.NewEncoder writes a trailing '\n' that we strip below.
	enc := stdjson.NewEncoder(buf)
	//: encode the value via the stdlib encoder.
	jerr := enc.Encode(v)
	//: encoding failure — keep dst pristine, surface the wrapped error.
	if jerr != nil {
		//: drop the buffer back to the pool if it's not oversized.
		scratch.ReleaseBuffer(buf)
		//: return the untouched buffer plus the wrapped error.
		return dst, errs.Wrap(jerr, errs.WrapParams{
			Code:    CodeJSONMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "JSON encoding failed",
			Private: "service/codec/json.Append: encoding/json returned an error",
		})
	}
	//: stdjson.Encoder.Encode always emits a trailing '\n' which is NOT
	//: part of the JSON value — strip it so Append's output matches
	//: Marshal's byte-for-byte (audit-pinned wire contract).
	encoded := buf.Bytes()
	//: defensive against an empty payload (shouldn't happen on success).
	if n := len(encoded); n > 0 && encoded[n-1] == '\n' {
		//: drop the trailing newline.
		encoded = encoded[:n-1]
	}
	//: copy into the caller's buffer; the only copy on the hot path.
	dst = append(dst, encoded...)
	//: cap-discard release.
	scratch.ReleaseBuffer(buf)
	//: success — bytes are the caller's now.
	return dst, nil
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
