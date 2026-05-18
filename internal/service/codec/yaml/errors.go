// Package yaml — declares the sentinel *errs.Error values for YAML.
package yaml

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure from gopkg.in/yaml.v3.Marshal.
	MarshalFailed = errs.Define(CodeYAMLMarshalFailed, "MARSHAL_FAILED",
		"YAML encoding failed",
		"service/codec/yaml: gopkg.in/yaml.v3.Marshal returned an error")

	// UnmarshalFailed wraps a failure from gopkg.in/yaml.v3.Unmarshal.
	UnmarshalFailed = errs.Define(CodeYAMLUnmarshalFailed, "UNMARSHAL_FAILED",
		"YAML decoding failed",
		"service/codec/yaml: gopkg.in/yaml.v3.Unmarshal returned an error")
)
