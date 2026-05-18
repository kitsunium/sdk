// Package json — adapts *encoding/json.Decoder to codec.Decoder.
package json

import (
	stdjson "encoding/json"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// jsonDecoder wraps *encoding/json.Decoder so it satisfies codec.Decoder.
type jsonDecoder struct {
	inner *stdjson.Decoder
}

// Decode reads the next value from the wrapped stdlib decoder.
func (d *jsonDecoder) Decode(v any) error {
	//: delegate to stdlib then wrap on error.
	jerr := d.inner.Decode(v)
	//: success fast-path.
	if jerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the stdlib error.
	return errs.Wrap(jerr, errs.WrapParams{
		Code:    CodeJSONUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "JSON decoding failed",
		Private: "service/codec/json.Decoder.Decode: encoding/json returned an error",
	})
}

// More reports whether another JSON value remains in the stream.
func (d *jsonDecoder) More() bool {
	//: delegate to the stdlib bool.
	return d.inner.More()
}
