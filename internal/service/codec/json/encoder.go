// Package json: encoder.go adapts *encoding/json.Encoder to codec.Encoder.
package json

import (
	stdjson "encoding/json"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// jsonEncoder wraps *encoding/json.Encoder so it satisfies codec.Encoder.
type jsonEncoder struct {
	inner *stdjson.Encoder
}

// Encode serialises v through the wrapped stdlib encoder.
//
// Params:
//   - v: value to encode.
//
// Returns:
//   - error: MarshalFailed wrapping the stdlib cause on failure; nil otherwise.
func (e *jsonEncoder) Encode(v any) (err error) {
	//: delegate to the stdlib then wrap on error.
	jerr := e.inner.Encode(v)
	//: success fast-path.
	if jerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the stdlib error.
	return errs.Wrap(jerr, errs.WrapParams{
		Code:    CodeMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "JSON encoding failed",
		Private: "service/codec/json.Encoder.Encode: encoding/json returned an error",
	})
}

// Close is a no-op because the stdlib encoder does not own the writer.
//
// Returns:
//   - error: always nil.
func (*jsonEncoder) Close() (err error) {
	//: stdlib encoder owns no writer-level state.
	return nil
}
