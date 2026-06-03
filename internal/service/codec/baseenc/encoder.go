// Package baseenc — adapts the base-N stream-writer pipeline to
// codec.Encoder. Each Encode call serialises one JSON value into the
// underlying base-N WriteCloser, which writes the encoded bytes onto the
// caller's io.Writer.
package baseenc

import (
	stdjson "encoding/json"
	"io"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// baseencEncoder wraps a base-N io.WriteCloser as a codec.Encoder.
// JSON serialisation is performed via a stdlib json.Encoder pointed at
// the base-N writer so the JSON output flows through the encoding stream
// without an intermediate buffer.
type baseencEncoder struct {
	base    io.WriteCloser
	jsonEnc *stdjson.Encoder
}

// Encode serialises v as JSON and feeds the bytes through the base-N writer.
func (e *baseencEncoder) Encode(v any) error {
	//: lazy-init the json encoder so a never-used encoder allocates nothing.
	if e.jsonEnc == nil {
		//: bind the json encoder to the base-N writer.
		e.jsonEnc = stdjson.NewEncoder(e.base)
	}
	//: delegate to stdlib then wrap on error.
	jerr := e.jsonEnc.Encode(v)
	//: success fast-path.
	if jerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the stdlib JSON error.
	return errs.Wrap(jerr, errs.WrapParams{
		Code:    CodeBaseEncMarshalFailed,
		Reason:  "BASE_ENC_MARSHAL_FAILED",
		Public:  "base-N encoding failed",
		Private: "service/codec/baseenc.Encoder.Encode: encoding/json returned an error",
	})
}

// Close flushes the underlying base-N writer so any padding tail bytes
// are emitted before the caller assumes the stream is complete.
func (e *baseencEncoder) Close() error {
	//: propagate the base-N writer's error verbatim wrapped by the marshal sentinel.
	cerr := e.base.Close()
	//: success fast-path.
	if cerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the base-N writer's failure.
	return errs.Wrap(cerr, errs.WrapParams{
		Code:    CodeBaseEncMarshalFailed,
		Reason:  "BASE_ENC_MARSHAL_FAILED",
		Public:  "base-N encoding failed",
		Private: "service/codec/baseenc.Encoder.Close: base-N writer returned an error",
	})
}
