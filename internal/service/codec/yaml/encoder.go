// Package yaml: encoder.go adapts yaml.v3's *Encoder to codec.Encoder.
package yaml

import (
	goyaml "gopkg.in/yaml.v3"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// yamlEncoder wraps *yaml.Encoder so it satisfies codec.Encoder.
type yamlEncoder struct {
	inner *goyaml.Encoder
}

// Encode serialises v through the wrapped yaml.v3 encoder.
//
// Params:
//   - v: value to encode.
//
// Returns:
//   - error: MarshalFailed wrapping the library cause on failure.
func (e *yamlEncoder) Encode(v any) error {
	//: delegate and wrap on error.
	yerr := e.inner.Encode(v)
	//: success fast-path.
	if yerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library error.
	return errs.Wrap(yerr, errs.WrapParams{
		Code:    CodeYAMLMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "YAML encoding failed",
		Private: "service/codec/yaml.Encoder.Encode: gopkg.in/yaml.v3 returned an error",
	})
}

// Close flushes the underlying encoder; yaml.v3 requires it for stream output.
//
// Returns:
//   - error: MarshalFailed wrapping the library cause on failure.
func (e *yamlEncoder) Close() error {
	//: delegate to the library; terminal '---' is emitted on flush.
	cerr := e.inner.Close()
	//: success fast-path.
	if cerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library error.
	return errs.Wrap(cerr, errs.WrapParams{
		Code:    CodeYAMLMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "YAML encoding failed",
		Private: "service/codec/yaml.Encoder.Close: gopkg.in/yaml.v3 returned an error",
	})
}
