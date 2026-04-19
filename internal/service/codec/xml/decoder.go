// Package xml: decoder.go adapts *encoding/xml.Decoder to codec.Decoder.
package xml

import (
	stdxml "encoding/xml"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// xmlDecoder wraps *encoding/xml.Decoder so it satisfies codec.Decoder.
type xmlDecoder struct {
	inner *stdxml.Decoder
}

// Decode reads the next value from the wrapped stdlib decoder.
//
// Params:
//   - v: pointer to the destination value.
//
// Returns:
//   - error: UnmarshalFailed wrapping the stdlib cause on failure; nil otherwise.
func (d *xmlDecoder) Decode(v any) (err error) {
	//: delegate.
	xerr := d.inner.Decode(v)
	//: success fast-path.
	if xerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap.
	return errs.Wrap(xerr, errs.WrapParams{
		Code:    CodeUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "XML decoding failed",
		Private: "service/codec/xml.Decoder.Decode: encoding/xml returned an error",
	})
}

// More reports whether another XML element can be decoded.
// The stdlib decoder has no More method; we approximate by checking if the
// next token is EOF.
//
// Returns:
//   - bool: true when a further Decode call is likely to succeed.
func (d *xmlDecoder) More() (ok bool) {
	//: peek at the next token — returning any token (or a skip-able one)
	//: means content remains. The stdlib consumes the token so we cannot
	//: push it back; callers that want a reliable More use their own token
	//: accounting.
	_, terr := d.inner.Token()
	//: any successful token read signals content remains.
	return terr == nil
}
