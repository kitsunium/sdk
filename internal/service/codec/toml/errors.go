// Package toml — declares the sentinel *errs.Error values for TOML.
package toml

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure from pelletier/go-toml/v2.Marshal.
	MarshalFailed = errs.Define(CodeTOMLMarshalFailed, "MARSHAL_FAILED",
		"TOML encoding failed",
		"service/codec/toml: github.com/pelletier/go-toml/v2.Marshal returned an error")

	// UnmarshalFailed wraps a failure from pelletier/go-toml/v2.Unmarshal.
	UnmarshalFailed = errs.Define(CodeTOMLUnmarshalFailed, "UNMARSHAL_FAILED",
		"TOML decoding failed",
		"service/codec/toml: github.com/pelletier/go-toml/v2.Unmarshal returned an error")
)
