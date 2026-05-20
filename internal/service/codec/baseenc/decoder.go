// Package baseenc — adapts the base-N stream-reader pipeline to
// codec.Decoder. Each Decode call drives a stdlib json.Decoder over the
// base-N decoded byte stream wrapped around the caller's io.Reader.
package baseenc

import (
	stdjson "encoding/json"
	"io"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// baseencDecoder wraps a base-N io.Reader as a codec.Decoder.
// The stdlib json.Decoder consumes the recovered bytes directly, so the
// inner pipeline is base-N reader → json.Decoder → caller's target.
type baseencDecoder struct {
	src     io.Reader
	jsonDec *stdjson.Decoder
	v       variant
}

// Decode reads the next JSON value from the base-N → JSON pipeline.
func (d *baseencDecoder) Decode(v any) error {
	//: lazy-init the json decoder so we obtain the base-N reader once.
	if d.jsonDec == nil {
		//: bind the json decoder to the base-N reader.
		d.jsonDec = stdjson.NewDecoder(d.codec().streamReader(d.src))
	}
	//: delegate to stdlib then wrap on error.
	jerr := d.jsonDec.Decode(v)
	//: success fast-path.
	if jerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the stdlib JSON error.
	return errs.Wrap(jerr, errs.WrapParams{
		Code:    CodeBaseEncUnmarshalFailed,
		Reason:  "BASE_ENC_UNMARSHAL_FAILED",
		Public:  "base-N decoding failed",
		Private: "service/codec/baseenc.Decoder.Decode: encoding/json returned an error",
	})
}

// More reports whether another JSON value remains in the stream.
func (d *baseencDecoder) More() bool {
	//: a never-used decoder reports false rather than panicking.
	if d.jsonDec == nil {
		//: caller has not driven Decode yet — assume input is available.
		return d.src != nil
	}
	//: delegate to stdlib.
	return d.jsonDec.More()
}

// codec returns a stub baseencCodec carrying the variant. The stub gives
// the decoder access to streamReader without holding a back-pointer to
// the registered singleton (which would couple the lifetime artificially).
func (d *baseencDecoder) codec() *baseencCodec {
	//: a stack-allocated stub is enough; baseencCodec is stateless.
	return &baseencCodec{variant: d.v}
}
