// Package yaml — declares the sentinel *errs.Error values for the full YAML codec.
package yaml

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed marks a gopkg.in/yaml.v3 encode failure.
	MarshalFailed = errs.Define(CodeYAMLFullMarshalFailed, "YAML_FULL_MARSHAL_FAILED",
		"YAML encoding failed",
		"third-party/codec/yaml: gopkg.in/yaml.v3 failed to encode the value")

	// UnmarshalFailed marks a gopkg.in/yaml.v3 decode failure, or an input over
	// the 10 MiB cap.
	UnmarshalFailed = errs.Define(CodeYAMLFullUnmarshalFailed, "YAML_FULL_UNMARSHAL_FAILED",
		"YAML decoding failed",
		"third-party/codec/yaml: gopkg.in/yaml.v3 failed to decode the document, or the input exceeds maxYAMLBytes")
)
