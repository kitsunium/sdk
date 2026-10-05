// Package codec is the universal encoder/decoder dispatch facade.
//
// One verb, twenty-four formats. [Marshal] and [Unmarshal] reach every
// encoding the SDK ships — JSON, YAML, CBOR, MessagePack, BSON, NDJSON,
// XML, TOML, CSV, urlencoded forms, multipart/form-data, ASN.1 DER, PEM,
// TLV, FlatBuffers,
// plus the nine base-N variants (base64, base64url, base32, base16,
// base45, base58, base62, hex, ascii85). Format-swap at runtime is a
// single string change.
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
//   - Zero registration code. Blank-import the package; the 16
//     codecs register themselves as Go initialises them. No Register()
//     calls in consumer code. A program that needs one format imports
//     that format's package beneath this one instead, and links it alone.
//   - Append for hot paths. Codecs implementing Appender let you
//     reuse a []byte buffer across calls — handy in tight loops.
//   - Broadcast marshal. MarshalMany(v, formats...) serialises the
//     same value to N formats at once — content negotiation, replication,
//     multi-protocol message buses.
//
// # What's shipped — all 24 formats
//
// Single blank import (`import _ "github.com/kitsunium/sdk/pkg/v1/data/codec"`)
// activates the entire list. The Streaming column marks codecs that
// implement core/data/codec.StreamingCodec and unlock NewEncoder /
// NewDecoder. Every codec implements core/data/codec.Appender.
//
//	| Format       | Go constant       | MIME                       | Extension           | Streaming | Best for |
//	|--------------|-------------------|----------------------------|---------------------|:---------:|----------|
//	| json         | codec.JSON        | application/json           | .json               | yes       | API responses, public configs |
//	| ndjson       | codec.NDJSON      | application/x-ndjson       | .ndjson / .jsonl    | —         | streaming logs, line-oriented batches |
//	| xml          | codec.XML         | application/xml            | .xml                | yes       | legacy integrations |
//	| csv          | codec.CSV         | text/csv                   | .csv                | —         | tabular exports |
//	| form         | codec.Form        | application/x-www-form-urlencoded | .form / .urlencoded | —  | HTML form bodies, query strings |
//	| asn1-der     | codec.ASN1DER     | application/pkix-cert      | .der / .cer         | —         | crypto / X.509 artefacts |
//	| pem          | codec.PEM         | application/x-pem-file     | .pem / .crt / .key  | —         | block-wrapped DER (certs, keys) |
//	| yaml         | codec.YAML        | application/yaml           | .yaml / .yml        | yes       | human-edited configs |
//	| toml         | codec.TOML        | application/toml           | .toml               | yes       | app configs (strict typing) |
//	| cbor         | codec.CBOR        | application/cbor           | .cbor               | yes       | IoT / mobile (RFC 8949) |
//	| msgpack      | codec.MsgPack     | application/msgpack        | .msgpack / .mpk     | yes       | RPC payloads |
//	| bson         | codec.BSON        | application/bson           | .bson               | —         | MongoDB documents (top level must be a document) |
//	| multipart    | codec.Multipart   | multipart/form-data        | —                   | yes       | uploads / form posts (native shape codec.MultipartForm) |
//	| tlv          | codec.TLV         | application/x-tlv          | .tlv                | yes       | custom binary streams, self-describing |
//	| flatbuffers  | codec.FlatBuffers | application/x-flatbuffers  | .fbs / .bin         | —         | zero-copy passthrough |
//	| base64       | codec.Base64      | application/base64         | .b64 / .base64      | yes       | text-safe wrap (JSON → base-N) |
//	| base64url    | codec.Base64URL   | application/base64url      | .b64url             | yes       | URL-safe base64 |
//	| base32       | codec.Base32      | application/base32         | .b32                | yes       | larger alphabet for human-typed tokens |
//	| base16       | codec.Base16      | application/base16         | .b16                | yes       | hex-like text (uppercase) |
//	| base45       | codec.Base45      | application/base45         | .b45                | yes       | QR-code alphanumeric payloads (RFC 9285) |
//	| base58       | codec.Base58      | application/base58         | .b58                | yes       | Bitcoin-style short IDs (4 KiB input cap) |
//	| base62       | codec.Base62      | application/base62         | .b62                | yes       | alphanumeric short IDs (4 KiB input cap) |
//	| hex          | codec.Hex         | application/hex            | .hex                | yes       | classic hex pair encoding (lowercase) |
//	| ascii85      | codec.ASCII85     | application/ascii85        | .a85                | yes       | 4-byte to 5-char text packing |
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
//	    "github.com/kitsunium/sdk/pkg/v1/data/codec"
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
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec"
//
// The package is the aggregate of the sixteen per-format packages beneath it
// — asn1, baseenc, bson, cbor, csv, flatbuffers, form, json, msgpack,
// multipart, ndjson, pem, tlv, toml, xml and yaml — and blank-imports each.
// Every one of them imports its codec, which registers itself in the registry
// as Go initialises it, once however many packages import it. Consumers never
// call a Register() function — registration is a side-effect. Importing one
// of those packages instead registers its format alone, links nothing of the
// others, and names that format's error codes (ADR 0134).
//
// # Extension interfaces
//
// A registered codec MAY implement one or both of these optional
// interfaces (assert at the call site):
//
//	// Append into a caller-supplied buffer — drops the fresh-slice
//	// allocation Marshal returns (see BENCH.md for per-codec numbers;
//	// the remaining allocs are the encoder's, not the facade's).
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
// codecs), which each per-format package names — cbor.CodeUnmarshalFailed,
// yaml.CodeAnchorRefused. Inspect with the accessors in
// github.com/kitsunium/sdk/pkg/v1/errs:
//
//	if errs.HasCode(err, codec.CodeUnknownFormat) { … }
//	if errs.HasReason(err, "UNKNOWN_FORMAT")       { … }
//
// Package codec — range 1.2.0.* (ADR 0005 pkg/v1/data/codec block).
//
// Package codec — compressed-frame verbs (ADR 0014 D1). MarshalCompressed and
// UnmarshalCompressed wrap the universal codec dispatch in a self-describing,
// version-tagged compression frame, so any value can be compressed in any
// registered Format without a parallel API. Compression is a parallel transform
// registry, never a codec Format (ADR 0014 §Why-not), so the algorithm is named
// separately from the wire Format.
//
// Package codec — declares the sentinel *errs.Error values the
// facade emits when dispatch fails.
//
// Package codec — the multipart/form-data value types, and the one helper a
// consumer needs to send what Marshal(Multipart, …) returns. The format is a
// container whose delimiter lives in the Content-Type header, which the Codec
// contract cannot carry, so the facade publishes the two shapes the codec
// speaks natively and the function that recovers that header from the body —
// the multipart package's own Form, Part and ContentType, under the names this
// package has always given them.
//
// Package codec — JSON-bridge promotion path for codecs whose runtime
// preconditions reject the public Marshal(F, any) / Unmarshal(F, *,
// any) contract. Six of the twenty-four registered codecs constrain
// their input shape: csv expects [][]string, ndjson expects []T, pem
// expects *pem.Block, flatbuffers expects []byte or BytesProvider,
// form expects url.Values, tlv's decoder cannot project composites
// into typed targets. Without promotion the facade's "format-swap is
// a single string change" promise is a lie for 6/24. Promotion
// intercepts the VALUE_INVALID / FLATBUFFERS_BAD_* / UNMARSHAL_FAILED
// responses, encodes the value to JSON, wraps the bytes in a
// codec-specific container the codec will accept, and reverses the
// pipeline on Unmarshal. The fast (native-shape) path is untouched so
// existing callers see zero overhead. See
// TestUniversalRoundtripAllCodecs for the contract pin.
package codec
