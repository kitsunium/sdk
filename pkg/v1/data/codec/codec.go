package codec

import (
	"errors"
	"io"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	//: the per-format packages, one per codec: each imports its service
	//: codec, which registers itself as it is initialised (ADR 0134). This
	//: package is their aggregate and registers nothing itself.
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/asn1"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/baseenc"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/bson"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/cbor"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/csv"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/flatbuffers"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/form"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/json"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/msgpack"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/multipart"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/ndjson"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/pem"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/tlv"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/toml"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/xml"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/yaml"
)

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
	// Form denotes application/x-www-form-urlencoded, the encoding every
	// HTML form POSTs. Its native Go shape is url.Values: a repeated key
	// carries multiple values, which is the only array syntax the format
	// has.
	Form Format = "form"
	// ASN1DER denotes the stdlib encoding/asn1 DER wire format.
	ASN1DER Format = "asn1-der"
	// PEM denotes the stdlib encoding/pem block format.
	PEM Format = "pem"
	// YAML denotes YAML as the SDK reads and writes it: a named subset of
	// YAML 1.2.2 on the standard library alone, which refuses anchors, tags,
	// merge keys and the other constructs it leaves out by name. The full
	// reader is the opt-in "yaml-full" Format of third-party/codec/yaml.
	YAML Format = "yaml"
	// TOML denotes TOML v1.0.0, read and written by the SDK's stdlib-only codec.
	TOML Format = "toml"
	// CBOR denotes the RFC 8949 wire format, encoded and decoded on the
	// standard library alone.
	CBOR Format = "cbor"
	// MsgPack denotes the MessagePack wire format (msgpack.org specification),
	// implemented natively on the standard library.
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
	// Base45 denotes the JSON-mediated RFC 9285 Base45 wrap (QR-code safe).
	Base45 Format = "base45"
	// Base58 denotes the JSON-mediated Bitcoin Base58 wrap (short inputs;
	// base-conversion is O(n²) so a 4 KiB input cap applies).
	Base58 Format = "base58"
	// Base62 denotes the JSON-mediated Base62 wrap (short inputs; same O(n²)
	// 4 KiB input cap as Base58).
	Base62 Format = "base62"
	// BSON denotes the MongoDB binary document format (top-level must be a
	// document — struct or map — not a scalar). ADR 0021.
	BSON Format = "bson"
	// Multipart denotes RFC 7578 multipart/form-data. Its native Go shape is
	// a [MultipartForm] of [MultipartPart] sections; any other value travels
	// as a single JSON-mediated part. The RFC 2046 boundary lives in the
	// Content-Type header, which the Codec contract cannot carry — the codec
	// re-emits it in the body, and [MultipartContentType] recovers the header
	// value from the bytes Marshal returned.
	Multipart Format = "multipart"
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
	//: slow path — only the constrained codecs have a promotion strategy;
	//: gating on hasPromotionStrategy keeps a value-rich codec's genuine
	//: fault routable instead of masking it as PROMOTE_FAILED (V75).
	if err != nil && hasPromotionStrategy(f) && isValueShapeMismatch(err) {
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
	//: slow path — only the constrained codecs have a promotion strategy;
	//: gating on hasPromotionStrategy lets a value-rich codec's corrupt-
	//: bytes UNMARSHAL_FAILED survive instead of being masked (V75).
	if uErr != nil && hasPromotionStrategy(f) && isValueShapeMismatch(uErr) {
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
	//: slow path — gate on hasPromotionStrategy so MarshalMany matches
	//: Marshal: only the constrained codecs retry; value-rich codecs
	//: forward their genuine fault instead of masking it (V75).
	if err != nil && hasPromotionStrategy(f) && isValueShapeMismatch(err) {
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
			Private: "pkg/v1/data/codec: codec " + c.Name() + " does not implement core/data/codec.StreamingCodec",
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
		Private: "pkg/v1/data/codec: no codec registered under Format " + string(f),
	})
}
