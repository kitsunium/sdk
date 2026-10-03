// Package toml — range 0.3.5.* (ADR 0005 service/data/codec/toml block).
package toml

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.5.0 - 0.3.5.255

// CodeTOMLMarshalFailed identifies a Go value the encoder cannot write as TOML.
const CodeTOMLMarshalFailed errs.Code = 0x00_03_05_01 // 0.3.5.1

// CodeTOMLUnmarshalFailed identifies a document the decoder cannot read, or a
// value its target cannot hold.
const CodeTOMLUnmarshalFailed errs.Code = 0x00_03_05_02 // 0.3.5.2
