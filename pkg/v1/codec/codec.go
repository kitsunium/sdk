// Package codec exposes the SDK's public codec surface. Consumers address
// codecs by Format (string alias) and dispatch through Marshal / Unmarshal /
// NewEncoder / NewDecoder. This package blank-imports every stdlib-backed
// codec so `import "github.com/kitsunium/sdk/pkg/v1/codec"` is enough to
// enable JSON, NDJSON, XML, CSV, ASN.1 DER, and PEM.
package codec

import (
	"io"

	corecodec "github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	//: blank imports drive each codec's self-registration side-effect.
	_ "github.com/kitsunium/sdk/internal/service/codec/asn1"
	_ "github.com/kitsunium/sdk/internal/service/codec/cbor"
	_ "github.com/kitsunium/sdk/internal/service/codec/csv"
	_ "github.com/kitsunium/sdk/internal/service/codec/json"
	_ "github.com/kitsunium/sdk/internal/service/codec/msgpack"
	_ "github.com/kitsunium/sdk/internal/service/codec/ndjson"
	_ "github.com/kitsunium/sdk/internal/service/codec/pem"
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
//
// Params:
//   - f: codec identifier.
//   - v: value to encode; codec-specific constraints apply.
//
// Returns:
//   - []byte: the encoded bytes.
//   - error: UnknownFormat when f is not registered; codec-specific errors otherwise.
func Marshal(f Format, v any) (data []byte, err error) {
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
//
// Params:
//   - f: codec identifier.
//   - data: encoded bytes.
//   - v: pointer destination; codec-specific constraints apply.
//
// Returns:
//   - error: UnknownFormat when f is not registered; codec-specific errors otherwise.
func Unmarshal(f Format, data []byte, v any) (err error) {
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
//
// Params:
//   - f: codec identifier.
//   - w: destination writer.
//
// Returns:
//   - Encoder: the streaming encoder when the codec supports streaming.
//   - error: UnknownFormat or StreamingUnsupported.
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
//
// Params:
//   - f: codec identifier.
//   - r: source reader.
//
// Returns:
//   - Decoder: the streaming decoder when the codec supports streaming.
//   - error: UnknownFormat or StreamingUnsupported.
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
//
// Returns:
//   - []Format: registered Formats (deterministic ordering).
func Available() (formats []Format) {
	//: delegate to the core registry.
	return corecodec.Available()
}

// FromMIME resolves a MIME string to its registered Format.
//
// Params:
//   - mime: MIME identifier (case-insensitive).
//
// Returns:
//   - Format: the resolved Format when ok.
//   - bool: true iff the MIME is registered.
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
//
// Params:
//   - ext: file extension including the leading dot (case-insensitive).
//
// Returns:
//   - Format: the resolved Format when ok.
//   - bool: true iff the extension is registered.
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
//
// Params:
//   - f: codec identifier.
//
// Returns:
//   - corecodec.StreamingCodec: the streaming-capable codec when err is nil.
//   - error: UnknownFormat when f is missing, StreamingUnsupported otherwise.
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
//
// Params:
//   - f: the Format the caller requested.
//
// Returns:
//   - error: the facade-level UnknownFormat sentinel.
func unknownFormat(f Format) (err error) {
	//: construct the error with an f-specific private diagnostic.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeUnknownFormat,
		Reason:  "UNKNOWN_FORMAT",
		Public:  "codec format is not registered",
		Private: "pkg/v1/codec: no codec registered under Format " + string(f),
	})
}
