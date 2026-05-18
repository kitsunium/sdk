// Package baseenc wraps Go's byte-encoding stdlibs (base64 / base32 / hex /
// ascii85) behind a uniform Encoding-tagged API. These are NOT structured
// codecs — they transform raw bytes to/from a textual alphabet and therefore
// live outside the core/codec Codec registry.
package baseenc

import (
	"bytes"
	"encoding/ascii85"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"io"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Encoding identifies one of the supported byte encodings. Encoded as a
// typed int with iota so consumers compare by identity
// rather than by string, and the compiler catches typos at call sites.
type Encoding int

// Known encodings. The numeric values are stable across releases — new
// encodings MUST be appended at the end of the iota block.
const (
	// EncodingUnknown is the zero value; reserved as the invalid sentinel.
	EncodingUnknown Encoding = iota
	// Base64Std uses the RFC 4648 standard alphabet with padding.
	Base64Std
	// Base64URL uses the RFC 4648 URL-safe alphabet with padding.
	Base64URL
	// Base32Std uses the RFC 4648 standard alphabet with padding.
	Base32Std
	// Base32Hex uses the RFC 4648 "extended hex" alphabet with padding.
	Base32Hex
	// Base16Hex uses lowercase hexadecimal encoding.
	Base16Hex
	// ASCII85 uses Adobe's Ascii85 encoding.
	ASCII85
)

// String implements fmt.Stringer for Encoding, returning the canonical
// short name used in diagnostics.
func (e Encoding) String() string {
	//: dispatch on the enum value so the textual form stays stable.
	switch e {
	//: zero value sentinel — explicit.
	case EncodingUnknown:
		//: documented "unknown" form mirrors the out-of-range fallback.
		return "unknown"
	//: standard base64 with padding.
	case Base64Std:
		//: canonical short name.
		return "base64"
	//: URL-safe base64.
	case Base64URL:
		//: canonical short name.
		return "base64url"
	//: standard base32.
	case Base32Std:
		//: canonical short name.
		return "base32"
	//: extended-hex base32.
	case Base32Hex:
		//: canonical short name.
		return "base32hex"
	//: lowercase hex.
	case Base16Hex:
		//: canonical short name.
		return "hex"
	//: ASCII85.
	case ASCII85:
		//: canonical short name.
		return "ascii85"
	}
	//: out-of-range Encoding values fall through to the "unknown" form.
	return "unknown"
}

// Encode encodes raw into text using the given Encoding.
func Encode(e Encoding, raw []byte) (text string, err error) {
	//: branch on the encoding identifier.
	switch e {
	//: zero value sentinel — explicit
	//: while still funnelling into the INVALID_ENCODING failure path.
	case EncodingUnknown:
		//: fall through to the typed facade error below.
	//: standard base64 with padding.
	case Base64Std:
		//: delegate to the stdlib.
		return base64.StdEncoding.EncodeToString(raw), nil
	//: URL-safe base64.
	case Base64URL:
		//: delegate to the stdlib.
		return base64.URLEncoding.EncodeToString(raw), nil
	//: standard base32.
	case Base32Std:
		//: delegate to the stdlib.
		return base32.StdEncoding.EncodeToString(raw), nil
	//: extended-hex base32.
	case Base32Hex:
		//: delegate to the stdlib.
		return base32.HexEncoding.EncodeToString(raw), nil
	//: lowercase hex.
	case Base16Hex:
		//: delegate to the stdlib.
		return hex.EncodeToString(raw), nil
	//: ASCII85.
	case ASCII85:
		//: Ascii85 has no string helper; encode through a buffer.
		return encodeASCII85(raw)
	}
	//: unknown encoding — surface the typed facade error.
	return "", errs.Wrap(nil, errs.WrapParams{
		Code:    CodeInvalidEncoding,
		Reason:  "INVALID_ENCODING",
		Public:  "baseenc encoding is not supported",
		Private: "pkg/v1/codec/baseenc.Encode: unknown Encoding " + e.String(),
	})
}

// Decode decodes text into raw bytes using the given Encoding.
func Decode(e Encoding, text string) (raw []byte, err error) {
	//: branch on the encoding identifier.
	switch e {
	//: zero value sentinel — explicit
	//: while still funnelling into the INVALID_ENCODING failure path.
	case EncodingUnknown:
		//: fall through to the typed facade error below.
	//: standard base64 with padding.
	case Base64Std:
		//: delegate and wrap stdlib errors.
		return wrapDecode(base64.StdEncoding.DecodeString(text))
	//: URL-safe base64.
	case Base64URL:
		//: delegate and wrap stdlib errors.
		return wrapDecode(base64.URLEncoding.DecodeString(text))
	//: standard base32.
	case Base32Std:
		//: delegate and wrap stdlib errors.
		return wrapDecode(base32.StdEncoding.DecodeString(text))
	//: extended-hex base32.
	case Base32Hex:
		//: delegate and wrap stdlib errors.
		return wrapDecode(base32.HexEncoding.DecodeString(text))
	//: lowercase hex.
	case Base16Hex:
		//: delegate and wrap stdlib errors.
		return wrapDecode(hex.DecodeString(text))
	//: ASCII85.
	case ASCII85:
		//: Ascii85 needs a reader; helper consumes it.
		return decodeASCII85(text)
	}
	//: unknown encoding — surface the typed facade error.
	return nil, errs.Wrap(nil, errs.WrapParams{
		Code:    CodeInvalidEncoding,
		Reason:  "INVALID_ENCODING",
		Public:  "baseenc encoding is not supported",
		Private: "pkg/v1/codec/baseenc.Decode: unknown Encoding " + e.String(),
	})
}

// encodeASCII85 returns the Ascii85 encoding of raw.
func encodeASCII85(raw []byte) (text string, err error) {
	//: bytes.Buffer preallocates via Grow so append-style growth is avoided.
	var buf bytes.Buffer
	//: size the buffer up-front for the theoretical maximum.
	buf.Grow(ascii85.MaxEncodedLen(len(raw)))
	//: stream through the stdlib encoder; Close flushes the tail bytes.
	w := ascii85.NewEncoder(&buf)
	//: propagate Write failures defensively (bytes.Buffer never errors).
	if _, werr := w.Write(raw); werr != nil {
		//: wrap with the Encode-side reason.
		return "", wrapASCII85Encode(werr)
	}
	//: Close emits the final partial group if any.
	if cerr := w.Close(); cerr != nil {
		//: wrap with the Encode-side reason.
		return "", wrapASCII85Encode(cerr)
	}
	//: hand back the buffered text.
	return buf.String(), nil
}

// wrapASCII85Encode wraps an encoder-side failure with the ENCODE_FAILED
// reason so callers can distinguish encode from decode errors via
// errs.HasReason.
func wrapASCII85Encode(cause error) error {
	//: single construction site keeps the Private message aligned.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeEncodeFailed,
		Reason:  "ENCODE_FAILED",
		Public:  "baseenc encoding failed",
		Private: "pkg/v1/codec/baseenc: ascii85 encoder returned an error",
	})
}

// decodeASCII85 decodes Ascii85-encoded text.
func decodeASCII85(text string) (raw []byte, err error) {
	//: Ascii85 tolerates surrounding whitespace; NewDecoder handles it.
	r := ascii85.NewDecoder(strings.NewReader(text))
	//: drain into a buffer.
	out, rerr := io.ReadAll(r)
	//: success path.
	if rerr == nil {
		//: hand back the decoded bytes.
		return out, nil
	}
	//: wrap the stdlib failure.
	return nil, errs.Wrap(rerr, errs.WrapParams{
		Code:    CodeDecodeFailed,
		Reason:  "DECODE_FAILED",
		Public:  "baseenc decoding failed",
		Private: "pkg/v1/codec/baseenc: ascii85 decoder returned an error",
	})
}

// wrapDecode adapts an (out, err) pair into the DecodeFailed sentinel.
func wrapDecode(out []byte, derr error) (raw []byte, err error) {
	//: success fast-path.
	if derr == nil {
		//: hand back the decoded bytes verbatim.
		return out, nil
	}
	//: wrap the stdlib failure with the facade-level reason.
	return nil, errs.Wrap(derr, errs.WrapParams{
		Code:    CodeDecodeFailed,
		Reason:  "DECODE_FAILED",
		Public:  "baseenc decoding failed",
		Private: "pkg/v1/codec/baseenc: stdlib decoder returned an error",
	})
}
