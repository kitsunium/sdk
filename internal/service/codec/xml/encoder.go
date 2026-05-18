// Package xml: encoder.go adapts *encoding/xml.Encoder to codec.Encoder.
package xml

import (
	stdxml "encoding/xml"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// xmlEncoder wraps *encoding/xml.Encoder so it satisfies codec.Encoder.
type xmlEncoder struct {
	inner *stdxml.Encoder
}

// Encode serialises v through the wrapped stdlib encoder.
//
// Params:
//   - v: value to encode.
//
// Returns:
//   - error: MarshalFailed wrapping the stdlib cause on failure; nil otherwise.
func (e *xmlEncoder) Encode(v any) error {
	//: delegate then wrap.
	xerr := e.inner.Encode(v)
	//: success fast-path.
	if xerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap.
	return errs.Wrap(xerr, errs.WrapParams{
		Code:    CodeXMLMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "XML encoding failed",
		Private: "service/codec/xml.Encoder.Encode: encoding/xml returned an error",
	})
}

// Close flushes the buffered stdlib encoder state.
//
// Returns:
//   - error: MarshalFailed wrapping the stdlib cause on failure; nil otherwise.
func (e *xmlEncoder) Close() error {
	//: stdlib encoder requires Flush to emit trailing data.
	xerr := e.inner.Flush()
	//: success fast-path.
	if xerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap.
	return errs.Wrap(xerr, errs.WrapParams{
		Code:    CodeXMLMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "XML encoding failed",
		Private: "service/codec/xml.Encoder.Close: Flush returned an error",
	})
}
