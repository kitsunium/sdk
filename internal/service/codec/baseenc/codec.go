// Package baseenc implements a family of codec.Codec wrappers around the
// stdlib byte encodings (base16 / base32 / base64 / ascii85). Each variant
// registers itself with the core/codec registry under a distinct Name —
// the variant discriminator is the registered Format, not a tagged enum.
//
// Universal Marshal flow: encode v with encoding/json, then base-N encode
// the resulting bytes. Unmarshal reverses the pipeline. The JSON layer is
// intentional — base-N alphabets are structureless, so flattening through
// JSON gives consumers a single Marshal/Unmarshal contract over any value.
//
// Blank-importing this package registers all six variants at process start
// via package-level var initialisers; no init() function involved.
package baseenc

import (
	"bytes"
	"encoding/ascii85"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	stdjson "encoding/json"
	"io"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxBaseEncBytes caps Unmarshal input at 10 MiB; bigger inputs surface
// CodeBaseEncSizeExceeded so memory-exhaustion attacks via huge base-N
// payloads (CWE-400) cannot reach the stdlib decoder.
const maxBaseEncBytes int = 10 * 1024 * 1024

// asciiLowerToUpperOffset is the bit-5 distance between an ASCII lowercase
// letter and its uppercase counterpart (e.g. 'a' - 'A' == 32).
const asciiLowerToUpperOffset = byte(32)

const (
	// variantBase64 is the RFC 4648 standard base64 alphabet with padding.
	variantBase64 variant = iota
	// variantBase64URL is the RFC 4648 URL-safe base64 alphabet with padding.
	variantBase64URL
	// variantBase32 is the RFC 4648 standard base32 alphabet with padding.
	variantBase32
	// variantBase16 is uppercase hexadecimal (RFC 4648).
	variantBase16
	// variantHex is lowercase hexadecimal — distinct codec from base16 to let
	// callers pick the alphabet via Format.
	variantHex
	// variantASCII85 is Adobe's Ascii85 encoding.
	variantASCII85
)

// variant enumerates the supported base-N variants. The values are an
// internal implementation detail — consumers address codecs by registered
// Name (the public Format), never by variant identity.
type variant int

// Singletons — registered with the core/codec registry at package load.
// Binding each registration result to a typed package-level var keeps us
// clear of init() and lets downstream callers refer to a specific variant.
var (
	// Base64 is the RFC 4648 standard base64 codec.
	Base64 codec.Codec = codec.Register(&baseencCodec{variant: variantBase64})
	// Base64URL is the RFC 4648 URL-safe base64 codec.
	Base64URL codec.Codec = codec.Register(&baseencCodec{variant: variantBase64URL})
	// Base32 is the RFC 4648 standard base32 codec.
	Base32 codec.Codec = codec.Register(&baseencCodec{variant: variantBase32})
	// Base16 is the uppercase hex codec.
	Base16 codec.Codec = codec.Register(&baseencCodec{variant: variantBase16})
	// Hex is the lowercase hex codec.
	Hex codec.Codec = codec.Register(&baseencCodec{variant: variantHex})
	// ASCII85 is the Adobe Ascii85 codec.
	ASCII85 codec.Codec = codec.Register(&baseencCodec{variant: variantASCII85})
)

// baseencCodec is the concrete codec.Codec implementation; one instance
// per variant. Stateless — safe for concurrent use.
type baseencCodec struct {
	variant variant
}

// Name returns the canonical Format identifier for the variant.
func (c *baseencCodec) Name() string {
	//: dispatch on the variant; the string is the public Format.
	return c.spec().name
}

// MIMETypes returns a defensive copy of the variant's MIME alias list.
func (c *baseencCodec) MIMETypes() []string {
	//: callers must not mutate the package-level table.
	return slices.Clone(c.spec().mimeTypes)
}

// Extensions returns a defensive copy of the variant's file extension list.
func (c *baseencCodec) Extensions() []string {
	//: callers must not mutate the package-level table.
	return slices.Clone(c.spec().extensions)
}

// Marshal serialises v via encoding/json, then base-N encodes the JSON
// bytes. Even for v of type []byte the JSON layer runs — encoding/json's
// []byte→base64-string convention applies inside the JSON payload before
// the outer base-N step wraps it.
func (c *baseencCodec) Marshal(v any) (encoded []byte, err error) {
	//: flatten the value through JSON first.
	jsonBytes, merr := stdjson.Marshal(v)
	//: surface JSON-side failures with the dedicated reason.
	if merr != nil {
		//: keep encoded nil so callers do not consume a partial buffer.
		return nil, errs.Wrap(merr, errs.WrapParams{
			Code:    CodeBaseEncMarshalFailed,
			Reason:  "BASE_ENC_MARSHAL_FAILED",
			Public:  "base-N encoding failed",
			Private: "service/codec/baseenc.Marshal: encoding/json.Marshal returned an error",
		})
	}
	//: apply the variant's base-N alphabet to the JSON bytes.
	return c.encodeBytes(jsonBytes), nil
}

// Unmarshal base-N decodes data, then parses the resulting JSON into v.
func (c *baseencCodec) Unmarshal(data []byte, v any) error {
	//: cap the input before allocating the decode buffer.
	if len(data) > maxBaseEncBytes {
		//: CWE-400 defence — refuse oversized inputs at the boundary.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeBaseEncSizeExceeded,
			Reason:  "BASE_ENC_SIZE_EXCEEDED",
			Public:  "base-N input exceeds size limit",
			Private: "service/codec/baseenc.Unmarshal: len(data) > maxBaseEncBytes",
		})
	}
	//: undo the base-N wrap; jsonBytes is the inner payload.
	jsonBytes, derr := c.decodeBytes(data)
	//: propagate base-N decode failures verbatim — they are already wrapped.
	if derr != nil {
		//: cause already carries the dotted-quad code.
		return derr
	}
	//: parse the JSON payload into the caller's target.
	uerr := stdjson.Unmarshal(jsonBytes, v)
	//: surface JSON-side failures with the dedicated reason.
	if uerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the stdlib JSON error.
	return errs.Wrap(uerr, errs.WrapParams{
		Code:    CodeBaseEncUnmarshalFailed,
		Reason:  "BASE_ENC_UNMARSHAL_FAILED",
		Public:  "base-N decoding failed",
		Private: "service/codec/baseenc.Unmarshal: encoding/json.Unmarshal returned an error",
	})
}

// Append encodes v via JSON then base-N appends the result onto dst.
// Implements the optional codec.Appender interface. The JSON step
// allocates intermediate bytes by necessity (encoding/json.Marshal does
// not expose an Append-style API), but the base-N step uses
// AppendEncode where the stdlib offers it so dst grows in place.
func (c *baseencCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: snapshot dst length so a JSON failure leaves the buffer untouched.
	origLen := len(dst)
	//: encode through JSON first — this is the unavoidable allocation.
	jsonBytes, merr := stdjson.Marshal(v)
	//: JSON-side failure restores dst and surfaces the dedicated reason.
	if merr != nil {
		//: leave dst exactly as the caller passed it.
		return dst[:origLen], errs.Wrap(merr, errs.WrapParams{
			Code:    CodeBaseEncMarshalFailed,
			Reason:  "BASE_ENC_MARSHAL_FAILED",
			Public:  "base-N encoding failed",
			Private: "service/codec/baseenc.Append: encoding/json.Marshal returned an error",
		})
	}
	//: base-N append uses the variant-specific AppendEncode where possible.
	return c.appendEncode(dst, jsonBytes), nil
}

// NewEncoder returns a streaming Encoder for the variant. The encoder
// serialises each value through encoding/json then wraps the bytes in a
// stdlib base-N stream sink. Implements codec.StreamingCodec.
func (c *baseencCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: wrap the base-N sink for the requested variant.
	return &baseencEncoder{
		base: c.streamWriter(w),
		v:    c.variant,
	}
}

// NewDecoder returns a streaming Decoder for the variant. The decoder
// reads the entire base-N stream, decodes it, then drives a json.Decoder
// over the recovered bytes. Implements codec.StreamingCodec.
func (c *baseencCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: wire the streaming pipeline lazily; first Decode does the work.
	return &baseencDecoder{
		src: r,
		v:   c.variant,
	}
}

// encodeBytes applies the variant's base-N alphabet to raw.
func (c *baseencCodec) encodeBytes(raw []byte) []byte {
	//: dispatch on the variant; each branch calls into the stdlib.
	switch c.variant {
	//: standard base64 with padding.
	case variantBase64:
		//: stdlib helper returns a freshly-allocated slice.
		encoded := make([]byte, base64.StdEncoding.EncodedLen(len(raw)))
		base64.StdEncoding.Encode(encoded, raw)
		//: hand back the encoded bytes verbatim.
		return encoded
	//: URL-safe base64 with padding.
	case variantBase64URL:
		//: stdlib helper returns a freshly-allocated slice.
		encoded := make([]byte, base64.URLEncoding.EncodedLen(len(raw)))
		base64.URLEncoding.Encode(encoded, raw)
		//: hand back the encoded bytes verbatim.
		return encoded
	//: standard base32 with padding.
	case variantBase32:
		//: stdlib helper returns a freshly-allocated slice.
		encoded := make([]byte, base32.StdEncoding.EncodedLen(len(raw)))
		base32.StdEncoding.Encode(encoded, raw)
		//: hand back the encoded bytes verbatim.
		return encoded
	//: hex variants — base16 is the upper-cased flavour, hex is lower-case.
	case variantBase16, variantHex:
		//: stdlib helper writes into a freshly-allocated slice.
		encoded := make([]byte, hex.EncodedLen(len(raw)))
		hex.Encode(encoded, raw)
		//: base16 wants the uppercase alphabet.
		if c.variant == variantBase16 {
			//: in-place upper-case keeps the allocation cost at one slice.
			return bytes.ToUpper(encoded)
		}
		//: hand back the lowercase form verbatim.
		return encoded
	//: Adobe ascii85.
	case variantASCII85:
		//: ascii85 has no AppendEncode; encode into a sized buffer.
		buf := make([]byte, ascii85.MaxEncodedLen(len(raw)))
		n := ascii85.Encode(buf, raw)
		//: slice off any trailing capacity past n.
		return buf[:n]
	}
	//: unreachable — Register only stores known variants.
	return nil
}

// decodeBytes reverses encodeBytes for the variant. Returns a wrapped
// error when the stdlib decoder rejects the input.
func (c *baseencCodec) decodeBytes(data []byte) (decoded []byte, err error) {
	//: cache the string form so repeated DecodeString calls share one alloc.
	text := string(data)
	//: dispatch on the variant.
	switch c.variant {
	//: standard base64 with padding.
	case variantBase64:
		//: stdlib returns an allocated slice or an error.
		return wrapDecode(base64.StdEncoding.DecodeString(text))
	//: URL-safe base64 with padding.
	case variantBase64URL:
		//: stdlib returns an allocated slice or an error.
		return wrapDecode(base64.URLEncoding.DecodeString(text))
	//: standard base32 with padding.
	case variantBase32:
		//: stdlib returns an allocated slice or an error.
		return wrapDecode(base32.StdEncoding.DecodeString(text))
	//: hex variants — encoding/hex accepts both letter cases on input.
	case variantBase16, variantHex:
		//: hex.DecodeString tolerates both letter cases on input.
		return wrapDecode(hex.DecodeString(text))
	//: Adobe ascii85.
	case variantASCII85:
		//: ascii85 needs a reader; drain it into a buffer.
		return decodeASCII85(data)
	}
	//: unreachable — Register only stores known variants.
	return nil, nil
}

// appendEncodeBase16Upper appends the uppercase-hex encoding of raw onto dst.
// Split out of appendEncode to keep the dispatch function under the cyclo
// budget and to give the per-byte upper-case loop a dedicated home.
func appendEncodeBase16Upper(dst, raw []byte) []byte {
	//: snapshot original length so we can upper-case just the new tail.
	startLen := len(dst)
	dst = hex.AppendEncode(dst, raw)
	tail := dst[startLen:]
	//: upper-case ASCII hex digits in-place.
	for i, b := range tail {
		//: only a..f need the 0x20 mask cleared.
		if b >= 'a' && b <= 'f' {
			//: bit 5 toggles case for ASCII letters.
			tail[i] = b - asciiLowerToUpperOffset
		}
	}
	//: hand back the now-uppercase buffer.
	return dst
}

// appendEncodeASCII85 appends the ascii85 encoding of raw onto dst.
// Split out of appendEncode because ascii85 has no stdlib AppendEncode
// helper — the encode-then-append shape is unique to this variant.
func appendEncodeASCII85(dst, raw []byte) []byte {
	//: encode into a sized buffer then append onto dst.
	buf := make([]byte, ascii85.MaxEncodedLen(len(raw)))
	n := ascii85.Encode(buf, raw)
	//: hand back the (possibly re-allocated) buffer.
	return append(dst, buf[:n]...)
}

// appendEncode appends the base-N encoding of raw onto dst, using the
// stdlib AppendEncode helpers where available and a fresh-buffer fallback
// for ascii85 which lacks the helper.
func (c *baseencCodec) appendEncode(dst, raw []byte) []byte {
	//: dispatch on the variant.
	switch c.variant {
	//: standard base64 with padding — Go 1.22+ AppendEncode.
	case variantBase64:
		//: stdlib appends into dst directly.
		return base64.StdEncoding.AppendEncode(dst, raw)
	//: URL-safe base64 with padding.
	case variantBase64URL:
		//: stdlib appends into dst directly.
		return base64.URLEncoding.AppendEncode(dst, raw)
	//: standard base32 with padding.
	case variantBase32:
		//: stdlib appends into dst directly.
		return base32.StdEncoding.AppendEncode(dst, raw)
	//: uppercase hex — encode lowercase, then convert in-place.
	case variantBase16:
		//: delegate to the dedicated helper to keep the cyclo budget healthy.
		return appendEncodeBase16Upper(dst, raw)
	//: lowercase hex — pure pass-through to the stdlib helper.
	case variantHex:
		//: stdlib appends into dst directly.
		return hex.AppendEncode(dst, raw)
	//: Adobe ascii85 — no AppendEncode helper.
	case variantASCII85:
		//: delegate to the dedicated helper to keep the cyclo budget healthy.
		return appendEncodeASCII85(dst, raw)
	}
	//: unreachable — Register only stores known variants.
	return dst
}

// streamWriter returns a base-N WriteCloser for the variant, wrapping w.
// Encoders for hex/ascii85 return their own io.WriteCloser; base32/64
// expose NewEncoder. base16 (uppercase) does not have a streaming form,
// so we fall back to a buffered encoder via the encodeBytes path.
func (c *baseencCodec) streamWriter(w io.Writer) io.WriteCloser {
	//: dispatch on the variant.
	switch c.variant {
	//: standard base64 with padding.
	case variantBase64:
		//: stdlib returns an io.WriteCloser.
		return base64.NewEncoder(base64.StdEncoding, w)
	//: URL-safe base64 with padding.
	case variantBase64URL:
		//: stdlib returns an io.WriteCloser.
		return base64.NewEncoder(base64.URLEncoding, w)
	//: standard base32 with padding.
	case variantBase32:
		//: stdlib returns an io.WriteCloser.
		return base32.NewEncoder(base32.StdEncoding, w)
	//: uppercase hex — buffer through encodeBytes since hex.NewEncoder is lowercase.
	case variantBase16:
		//: bufferingWriter accumulates writes then encodes on Close.
		return &bufferingWriter{dst: w, codec: c}
	//: lowercase hex.
	case variantHex:
		//: hex.NewEncoder writes lowercase digits directly.
		return nopWriteCloser{Writer: hex.NewEncoder(w)}
	//: Adobe ascii85.
	case variantASCII85:
		//: stdlib returns an io.WriteCloser.
		return ascii85.NewEncoder(w)
	}
	//: unreachable — Register only stores known variants.
	return nopWriteCloser{Writer: w}
}

// streamReader returns a base-N io.Reader for the variant, wrapping r.
func (c *baseencCodec) streamReader(r io.Reader) io.Reader {
	//: dispatch on the variant.
	switch c.variant {
	//: standard base64 with padding.
	case variantBase64:
		//: stdlib returns an io.Reader.
		return base64.NewDecoder(base64.StdEncoding, r)
	//: URL-safe base64 with padding.
	case variantBase64URL:
		//: stdlib returns an io.Reader.
		return base64.NewDecoder(base64.URLEncoding, r)
	//: standard base32 with padding.
	case variantBase32:
		//: stdlib returns an io.Reader.
		return base32.NewDecoder(base32.StdEncoding, r)
	//: hex variants — stdlib hex decoder accepts both letter cases on input.
	case variantBase16, variantHex:
		//: stdlib returns an io.Reader.
		return hex.NewDecoder(r)
	//: Adobe ascii85.
	case variantASCII85:
		//: stdlib returns an io.Reader.
		return ascii85.NewDecoder(r)
	}
	//: unreachable — Register only stores known variants.
	return r
}

// wrapDecode adapts a (bytes, error) pair to the base-N decode sentinel.
func wrapDecode(out []byte, derr error) (decoded []byte, err error) {
	//: success fast-path.
	if derr == nil {
		//: hand back the decoded bytes verbatim.
		return out, nil
	}
	//: wrap the stdlib failure with the base-N reason.
	return nil, errs.Wrap(derr, errs.WrapParams{
		Code:    CodeBaseEncDecodeFailed,
		Reason:  "BASE_ENC_DECODE_FAILED",
		Public:  "base-N decoding failed",
		Private: "service/codec/baseenc: stdlib base-N decoder returned an error",
	})
}

// decodeASCII85 drains an ascii85-encoded byte slice into a fresh buffer.
func decodeASCII85(data []byte) (decoded []byte, err error) {
	//: NewDecoder tolerates surrounding whitespace per the format spec.
	r := ascii85.NewDecoder(strings.NewReader(string(data)))
	//: drain into a buffer.
	decoded, rerr := io.ReadAll(r)
	//: success fast-path.
	if rerr == nil {
		//: hand back the decoded bytes.
		return decoded, nil
	}
	//: wrap the stdlib failure with the base-N reason.
	return nil, errs.Wrap(rerr, errs.WrapParams{
		Code:    CodeBaseEncDecodeFailed,
		Reason:  "BASE_ENC_DECODE_FAILED",
		Public:  "base-N decoding failed",
		Private: "service/codec/baseenc: ascii85 decoder returned an error",
	})
}
