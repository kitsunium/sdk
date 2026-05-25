//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/codec .

// Package codec is the universal encoder/decoder dispatch facade.
//
// One verb, eighteen formats. [Marshal] and [Unmarshal] reach every
// encoding the SDK ships — JSON, YAML, CBOR, MessagePack, NDJSON, XML,
// TOML, CSV, ASN.1 DER, PEM, TLV, FlatBuffers, plus the six base-N
// variants (base64, base64url, base32, base16, hex, ascii85).
// Format-swap at runtime is a single string change.
//
// # Goals
//
//   - One verb, many formats. codec.Marshal(f, v) reaches every
//     registered wire format — your call site doesn't change when
//     you swap JSON for CBOR.
//   - Stable Format strings. Once registered, a Format string is
//     frozen across minor versions. Code that compiles today keeps
//     compiling.
//   - Streaming when it pays. Codecs that implement StreamingCodec
//     get NewEncoder / NewDecoder automatically — no need to buffer
//     megabytes.
//   - Zero registration code. Blank-import the package; the 13
//     service codecs self-register via init(). No Register() calls
//     in consumer code.
//   - Append for hot paths. Codecs implementing Appender let you
//     reuse a []byte buffer across calls — handy in tight loops.
//   - Broadcast marshal. MarshalMany(v, formats...) serialises the
//     same value to N formats at once — content negotiation, replication,
//     multi-protocol message buses.
//
// # What's shipped — all 18 formats
//
// Single blank import (`import _ "github.com/kitsunium/sdk/pkg/v1/codec"`)
// activates the entire list. The Streaming column marks codecs that
// implement core/codec.StreamingCodec and unlock NewEncoder /
// NewDecoder.
//
//	| Format       | Go constant    | MIME                       | Extension       | Streaming | Best for |
//	|--------------|----------------|----------------------------|-----------------|:---------:|----------|
//	| json         | codec.JSON     | application/json           | .json           | yes       | API responses, public configs |
//	| ndjson       | codec.NDJSON   | application/x-ndjson       | .ndjson         | yes       | streaming logs, line-oriented batches |
//	| xml          | codec.XML      | application/xml            | .xml            | yes       | legacy integrations |
//	| csv          | codec.CSV      | text/csv                   | .csv            | yes       | tabular exports |
//	| asn1-der     | codec.ASN1DER  | application/pkix-cert      | .der            | —         | crypto / X.509 artefacts |
//	| pem          | codec.PEM      | application/x-pem-file     | .pem            | —         | block-wrapped DER (certs, keys) |
//	| yaml         | codec.YAML     | application/yaml           | .yaml / .yml    | yes       | human-edited configs |
//	| toml         | codec.TOML     | application/toml           | .toml           | yes       | app configs (strict typing) |
//	| cbor         | codec.CBOR     | application/cbor           | .cbor           | yes       | IoT / mobile (RFC 8949) |
//	| msgpack      | codec.MsgPack  | application/msgpack        | .msgpack        | yes       | RPC payloads |
//	| tlv          | "tlv"          | application/x-tlv          | —               | —         | custom binary streams, self-describing |
//	| flatbuffers  | "flatbuffers"  | application/x-flatbuffers  | .fbs            | —         | zero-copy passthrough |
//	| base64       | "base64"       | —                          | —               | —         | text-safe wrap (JSON → base-N) |
//	| base64url    | "base64url"    | —                          | —               | —         | URL-safe base64 |
//	| base32       | "base32"       | —                          | —               | —         | larger alphabet for human-typed tokens |
//	| base16       | "base16"       | —                          | —               | —         | hex-like text |
//	| hex          | "hex"          | —                          | —               | —         | classic hex pair encoding |
//	| ascii85      | "ascii85"      | —                          | —               | —         | 4-byte to 5-char text packing |
//
// Runtime introspection: codec.Available() returns the live registry
// list, codec.FromMIME and codec.FromExtension resolve from external
// metadata.
//
// # Quick start
//
//	package main
//
//	import (
//	    "fmt"
//
//	    "github.com/kitsunium/sdk/pkg/v1/codec"
//	)
//
//	type User struct {
//	    Name string `json:"name" cbor:"name" yaml:"name"`
//	    Age  int    `json:"age"  cbor:"age"  yaml:"age"`
//	}
//
//	func main() {
//	    u := User{Name: "Ada", Age: 36}
//	    for _, f := range []codec.Format{codec.JSON, codec.CBOR, codec.YAML, codec.MsgPack, codec.Base64} {
//	        data, _ := codec.Marshal(f, u)
//	        var back User
//	        _ = codec.Unmarshal(f, data, &back)
//	        fmt.Printf("%-10s %d bytes  → %#v\n", f, len(data), back)
//	    }
//	}
//
// # Activation
//
// A single blank import activates the full registry:
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/codec"
//
// The package blank-imports every internal/service/codec/* package;
// each registers itself in core/codec.Lookup via init(). Consumers
// never call a Register() function — registration is a side-effect.
//
// # Extension interfaces
//
// A registered codec MAY implement one or both of these optional
// interfaces (assert at the call site):
//
//	// Append into a caller-supplied buffer — zero-alloc fast path.
//	type Appender interface {
//	    Append(dst []byte, v any) ([]byte, error)
//	}
//
//	// Streaming — open Encoder/Decoder around an io.Writer/io.Reader.
//	type StreamingCodec interface {
//	    NewEncoder(w io.Writer) Encoder
//	    NewDecoder(r io.Reader) Decoder
//	}
//
// [StreamingUnsupported] is returned from [NewEncoder] / [NewDecoder]
// when the resolved codec does not satisfy StreamingCodec.
//
// # Errors
//
// Dispatch failures carry typed dotted-quad codes under range 1.2.0.*:
//
//   - 1.2.0.1 — [CodeUnknownFormat]: Available() does not list the requested Format.
//   - 1.2.0.2 — [CodeCodecUnavailable]: reserved for future build-tag gating.
//   - 1.2.0.3 — [CodeStreamingUnsupported]: NewEncoder / NewDecoder called on a non-streaming codec.
//
// Per-codec failures carry the codec's own range (0.3.* for service
// codecs). Inspect with the accessors in github.com/kitsunium/sdk/pkg/v1/errs:
//
//	if errs.HasCode(err, codec.CodeUnknownFormat) { … }
//	if errs.HasReason(err, "UNKNOWN_FORMAT")       { … }
package codec

import (
	"errors"
	"io"

	corecodec "github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	//: blank imports drive each codec's self-registration side-effect.
	_ "github.com/kitsunium/sdk/internal/service/codec/asn1"
	_ "github.com/kitsunium/sdk/internal/service/codec/baseenc"
	_ "github.com/kitsunium/sdk/internal/service/codec/cbor"
	_ "github.com/kitsunium/sdk/internal/service/codec/csv"
	_ "github.com/kitsunium/sdk/internal/service/codec/flatbuffers"
	_ "github.com/kitsunium/sdk/internal/service/codec/json"
	_ "github.com/kitsunium/sdk/internal/service/codec/msgpack"
	_ "github.com/kitsunium/sdk/internal/service/codec/ndjson"
	_ "github.com/kitsunium/sdk/internal/service/codec/pem"
	_ "github.com/kitsunium/sdk/internal/service/codec/tlv"
	_ "github.com/kitsunium/sdk/internal/service/codec/toml"
	_ "github.com/kitsunium/sdk/internal/service/codec/xml"
	_ "github.com/kitsunium/sdk/internal/service/codec/yaml"
)

// Format re-exports core/codec.Format so consumers only depend on pkg/v1.
type Format = corecodec.Format

// Codec re-exports core/codec.Codec for consumers who want direct access.
type Codec = corecodec.Codec

// Encoder re-exports core/codec.Encoder for streaming callers.
type Encoder = corecodec.Encoder

// Decoder re-exports core/codec.Decoder for streaming callers.
type Decoder = corecodec.Decoder

// Known format constants — string values are part of the public contract.
// Prefer the typed constants over string literals at call sites: the
// IDE catches typos at compile time, autocomplete surfaces the full
// list, and the underlying string value (frozen post-v1.0.0) stays
// available via `string(codec.JSON)` whenever raw access is needed.
const (
	// JSON denotes the stdlib encoding/json wire format.
	JSON Format = "json"
	// NDJSON denotes the newline-delimited JSON dialect.
	NDJSON Format = "ndjson"
	// XML denotes the stdlib encoding/xml wire format.
	XML Format = "xml"
	// CSV denotes the stdlib encoding/csv wire format.
	CSV Format = "csv"
	// ASN1DER denotes the stdlib encoding/asn1 DER wire format.
	ASN1DER Format = "asn1-der"
	// PEM denotes the stdlib encoding/pem block format.
	PEM Format = "pem"
	// YAML denotes the gopkg.in/yaml.v3 wire format.
	YAML Format = "yaml"
	// TOML denotes the pelletier/go-toml/v2 wire format.
	TOML Format = "toml"
	// CBOR denotes the fxamacker/cbor/v2 wire format (RFC 8949).
	CBOR Format = "cbor"
	// MsgPack denotes the vmihailenco/msgpack/v5 wire format.
	MsgPack Format = "msgpack"

	// TLV denotes the self-describing Type-Length-Value reflection
	// codec (one record per encode; nested composites supported).
	TLV Format = "tlv"
	// FlatBuffers denotes the passthrough codec for already-encoded
	// FlatBuffer payloads — schema-typed reads stay in the caller's
	// flatc-generated accessors.
	FlatBuffers Format = "flatbuffers"

	// Base64 denotes the JSON-mediated base64 (std) text-safe wrap.
	Base64 Format = "base64"
	// Base64URL denotes the JSON-mediated base64 (URL-safe alphabet) wrap.
	Base64URL Format = "base64url"
	// Base32 denotes the JSON-mediated base32 (std) text-safe wrap.
	Base32 Format = "base32"
	// Base16 denotes the JSON-mediated base16 (uppercase hex) wrap.
	Base16 Format = "base16"
	// Hex denotes the JSON-mediated lowercase-hex text-safe wrap.
	Hex Format = "hex"
	// ASCII85 denotes the JSON-mediated Adobe Ascii85 text-safe wrap.
	ASCII85 Format = "ascii85"
)

// Marshal serialises v using the codec registered under f. The codec's
// native input shape is tried first (fast path, zero overhead); if the
// codec rejects v as the wrong shape (csv requires [][]string, pem
// requires *pem.Block, etc.) the facade promotes v via json-encode +
// codec-specific wrap so every Format accepts any Go value — see
// promote.go for the per-format strategies and the uniform-contract
// rationale.
func Marshal(f Format, v any) (encoded []byte, err error) {
	//: resolve the codec before delegating.
	c, ok := corecodec.Lookup(f)
	//: dispatch miss returns a facade-level error.
	if !ok {
		//: caller used an unregistered Format.
		return nil, unknownFormat(f)
	}
	//: fast path — try the codec's native input shape first.
	encoded, err = c.Marshal(v)
	//: slow path — codec rejected v's shape; retry through the
	//: JSON-bridge promotion so the public contract holds for any v.
	if err != nil && isValueShapeMismatch(err) {
		//: promotion handles its own error wrapping.
		return promoteMarshal(f, c, v)
	}
	//: success or unrelated error — pass through unchanged.
	return encoded, err
}

// Unmarshal parses data into v using the codec registered under f. As
// with Marshal, the codec's native target shape is tried first; on
// shape mismatch the facade promotes via JSON-bridge so every Format
// can decode into any Go target.
func Unmarshal(f Format, data []byte, v any) error {
	//: resolve the codec before delegating.
	c, ok := corecodec.Lookup(f)
	//: dispatch miss returns a facade-level error.
	if !ok {
		//: caller used an unregistered Format.
		return unknownFormat(f)
	}
	//: fast path — try the codec's native target shape first.
	uErr := c.Unmarshal(data, v)
	//: slow path — codec rejected v's shape; retry through the
	//: JSON-bridge promotion so the public contract holds for any v.
	if uErr != nil && isValueShapeMismatch(uErr) {
		//: promotion handles its own error wrapping.
		return promoteUnmarshal(f, c, data, v)
	}
	//: success or unrelated error — pass through unchanged.
	return uErr
}

// MarshalMany serialises v into every format in formats and returns a
// map keyed by Format with the encoded bytes. The same logical value
// goes out in N different wire encodings — handy for HTTP content
// negotiation, multi-protocol message buses, archival doubling, or
// cross-region replication where each region speaks a different
// codec.
//
// Per-format errors are joined under [errors.Join] and returned as a
// single error; partial results stay in the returned map so callers
// can decide whether to ship the formats that succeeded or fail the
// whole batch. An unknown Format in the list surfaces
// [UnknownFormat] / [CodeUnknownFormat] inside that joined error
// (and leaves no entry in the map for that Format).
//
// Calling with formats == nil (or empty) returns an empty map and no
// error — the no-op semantics mirror calling Marshal zero times.
//
//	out, err := codec.MarshalMany(payload, codec.JSON, codec.CBOR, codec.MsgPack)
//	// out[codec.JSON], out[codec.CBOR], out[codec.MsgPack] all populated
//	// when err == nil.
func MarshalMany(v any, formats ...Format) (encodedByFormat map[Format][]byte, err error) {
	//: pre-allocate the result map with exact cardinality.
	encodedByFormat = make(map[Format][]byte, len(formats))
	//: accumulator for per-format failures so partial success is visible.
	var perFormat []error
	//: iterate in caller order so a misordered formats list still yields
	//: a deterministic out map. resolve-once: Lookup the codec for each
	//: Format up-front, dispatch c.Marshal(v) directly. Saves one
	//: Marshal func-entry frame per format vs the old per-iter
	//: Marshal(f, v) call (it would do the same Lookup internally).
	for _, f := range formats {
		//: resolve once — same path Marshal would take, no double-Lookup.
		c, ok := corecodec.Lookup(f)
		//: unknown Format → typed sentinel, no encode attempt.
		if !ok {
			//: caller used an unregistered Format.
			perFormat = append(perFormat, unknownFormat(f))
			//: absence in the out map signals "not encoded" for this Format.
			continue
		}
		//: direct codec dispatch; the registry already validated f.
		data, mErr := encodeWithPromotion(f, c, v)
		//: failure path — keep going so the caller sees every formats result.
		if mErr != nil {
			//: append the typed sentinel so HasCode(err, CodeUnknownFormat) keeps working through Join.
			perFormat = append(perFormat, mErr)
			//: don't record a partial bytes entry for this Format — absence is the signal.
			continue
		}
		//: success — record the bytes under the Format key.
		encodedByFormat[f] = data
	}
	//: collapse the per-format failure slice into a single joined error or nil.
	if len(perFormat) > 0 {
		//: errors.Join walks unwrap chains so HasCode keeps working.
		return encodedByFormat, errors.Join(perFormat...)
	}
	//: clean exit — every requested Format encoded successfully.
	return encodedByFormat, nil
}

// encodeWithPromotion runs the same fast-path + promote-fallback the
// public Marshal does, but takes a pre-resolved Codec so MarshalMany
// doesn't pay a second Lookup per format. Identical wire output to
// Marshal on every Format / value combination.
func encodeWithPromotion(f Format, c corecodec.Codec, v any) (encoded []byte, err error) {
	//: fast path — try the codec's native input shape first.
	encoded, err = c.Marshal(v)
	//: slow path — codec rejected v's shape; retry through the
	//: JSON-bridge promotion so the public contract holds for any v.
	if err != nil && isValueShapeMismatch(err) {
		//: promotion handles its own error wrapping.
		return promoteMarshal(f, c, v)
	}
	//: success or unrelated error — pass through unchanged.
	return encoded, err
}

// NewEncoder returns a streaming encoder for the codec registered under f.
func NewEncoder(f Format, w io.Writer) (enc Encoder, err error) {
	//: resolve + type-assert the streaming extension.
	sc, rerr := resolveStreaming(f)
	//: propagate the miss / wrong-shape failure.
	if rerr != nil {
		//: typed error already constructed.
		return nil, rerr
	}
	//: delegate to the streaming codec.
	return sc.NewEncoder(w), nil
}

// NewDecoder returns a streaming decoder for the codec registered under f.
func NewDecoder(f Format, r io.Reader) (dec Decoder, err error) {
	//: resolve + type-assert the streaming extension.
	sc, rerr := resolveStreaming(f)
	//: propagate the miss / wrong-shape failure.
	if rerr != nil {
		//: typed error already constructed.
		return nil, rerr
	}
	//: delegate to the streaming codec.
	return sc.NewDecoder(r), nil
}

// Available returns the sorted list of registered formats.
func Available() []Format {
	//: delegate to the core registry.
	return corecodec.Available()
}

// FromMIME resolves a MIME string to its registered Format.
func FromMIME(mime string) (f Format, ok bool) {
	//: delegate to the core registry; unwrap the Codec into its Format.
	c, found := corecodec.LookupMIME(mime)
	//: absence path.
	if !found {
		//: caller will treat the empty Format as invalid.
		return "", false
	}
	//: Codec.Name() is authoritative.
	return Format(c.Name()), true
}

// FromExtension resolves a file extension to its registered Format.
func FromExtension(ext string) (f Format, ok bool) {
	//: delegate to the core registry; unwrap the Codec into its Format.
	c, found := corecodec.LookupExt(ext)
	//: absence path.
	if !found {
		//: caller will treat the empty Format as invalid.
		return "", false
	}
	//: Codec.Name() is authoritative.
	return Format(c.Name()), true
}

// resolveStreaming resolves f and returns its StreamingCodec shape, or a
// typed facade error explaining why it could not.
func resolveStreaming(f Format) (sc corecodec.StreamingCodec, err error) {
	//: resolve the codec.
	c, found := corecodec.Lookup(f)
	//: format miss first.
	if !found {
		//: surface the facade-level sentinel.
		return nil, unknownFormat(f)
	}
	//: type-assert the streaming extension; non-streaming codecs fall through.
	stream, supports := c.(corecodec.StreamingCodec)
	//: wrong-shape path.
	if !supports {
		//: loud failure with a codec-specific private message.
		return nil, errs.Wrap(nil, errs.WrapParams{
			Code:    CodeStreamingUnsupported,
			Reason:  "STREAMING_UNSUPPORTED",
			Public:  "codec does not support streaming",
			Private: "pkg/v1/codec: codec " + c.Name() + " does not implement core/codec.StreamingCodec",
		})
	}
	//: streaming-capable codec.
	return stream, nil
}

// unknownFormat builds the typed UnknownFormat error for f.
func unknownFormat(f Format) error {
	//: construct the error with an f-specific private diagnostic.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeUnknownFormat,
		Reason:  "UNKNOWN_FORMAT",
		Public:  "codec format is not registered",
		Private: "pkg/v1/codec: no codec registered under Format " + string(f),
	})
}
