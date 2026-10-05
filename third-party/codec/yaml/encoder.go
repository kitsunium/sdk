// Package yaml — adapts yaml.v3's *Encoder to codec.Encoder.
//
// Package yaml — declares the sentinel *errs.Error values for the full YAML codec.
package yaml

import (
	goyaml "gopkg.in/yaml.v3"
)

// yamlEncoder wraps *yaml.Encoder so it satisfies codec.Encoder.
type yamlEncoder struct {
	inner *goyaml.Encoder
}

// Encode serialises v through the wrapped yaml.v3 encoder.
func (e *yamlEncoder) Encode(v any) error {
	//: delegate and wrap on error.
	yerr := e.inner.Encode(v)
	//: success fast-path.
	if yerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library error.
	return marshalFailed(yerr, "third-party/codec/yaml.Encoder.Encode: gopkg.in/yaml.v3 returned an error")
}

// Close flushes the underlying encoder; yaml.v3 requires it for stream output.
func (e *yamlEncoder) Close() error {
	//: delegate to the library; terminal state is emitted on flush.
	cerr := e.inner.Close()
	//: success fast-path.
	if cerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library error.
	return marshalFailed(cerr, "third-party/codec/yaml.Encoder.Close: gopkg.in/yaml.v3 returned an error")
}
