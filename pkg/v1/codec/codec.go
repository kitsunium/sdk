//go:generate gomarkdoc --output README.md .

// Package codec is the universal encoder/decoder dispatch facade.
//
// One verb, eighteen formats. [Marshal] and [Unmarshal] reach every
// encoding the SDK ships — JSON, YAML, CBOR, MessagePack, NDJSON, XML,
// TOML, CSV, ASN.1 DER, PEM, TLV, FlatBuffers, plus the six base-N
// variants (base64, base64url, base32, base16, hex, ascii85).
// Format-swap at runtime is a single string change.
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
//	    for _, f := range []codec.Format{"json", "cbor", "yaml", "msgpack", "base64"} {
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
)

// Marshal serialises v using the codec registered under f.
func Marshal(f Format, v any) (encoded []byte, err error) {
	//: resolve the codec before delegating.
	c, ok := corecodec.Lookup(f)
	//: dispatch miss returns a facade-level error.
	if !ok {
		//: caller used an unregistered Format.
		return nil, unknownFormat(f)
	}
	//: delegate to the concrete codec; its errors are already wrapped.
	return c.Marshal(v)
}

// Unmarshal parses data into v using the codec registered under f.
func Unmarshal(f Format, data []byte, v any) error {
	//: resolve the codec before delegating.
	c, ok := corecodec.Lookup(f)
	//: dispatch miss returns a facade-level error.
	if !ok {
		//: caller used an unregistered Format.
		return unknownFormat(f)
	}
	//: delegate to the concrete codec; its errors are already wrapped.
	return c.Unmarshal(data, v)
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
