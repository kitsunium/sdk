// Package xml: decoder.go adapts *encoding/xml.Decoder to codec.Decoder.
package xml

import (
	stdxml "encoding/xml"
	"errors"
	"io"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// xmlDecoder wraps *encoding/xml.Decoder so it satisfies codec.Decoder.
// A sticky done latch mirrors the contract of the other streaming codecs:
// More() returns false once Decode has surfaced io.EOF, without consuming
// any additional tokens from the underlying stream.
type xmlDecoder struct {
	inner *stdxml.Decoder
	//: sticky EOF flag so More() returns false after the stream drains.
	done bool
}

// Decode reads the next value from the wrapped stdlib decoder.
//
// Params:
//   - v: pointer to the destination value.
//
// Returns:
//   - error: UnmarshalFailed wrapping the stdlib cause on failure; io.EOF
//     (unwrapped) once the stream is drained; nil otherwise.
func (d *xmlDecoder) Decode(v any) (err error) {
	//: drained latch short-circuits additional reads.
	if d.done {
		//: mirror stdlib stream semantics.
		return io.EOF
	}
	//: delegate.
	xerr := d.inner.Decode(v)
	//: EOF latch toggles the done flag for subsequent More() calls.
	if errors.Is(xerr, io.EOF) {
		//: remember that we reached the stream end.
		d.done = true
		//: return io.EOF untouched.
		return io.EOF
	}
	//: success fast-path.
	if xerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap.
	return errs.Wrap(xerr, errs.WrapParams{
		Code:    CodeXMLUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "XML decoding failed",
		Private: "service/codec/xml.Decoder.Decode: encoding/xml returned an error",
	})
}

// More reports whether another XML element can be decoded.
//
// Returns:
//   - bool: false once Decode has returned io.EOF.
func (d *xmlDecoder) More() (ok bool) {
	//: reflect the sticky EOF latch — no token consumed.
	return !d.done
}
