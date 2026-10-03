// Package toml declares the error codes and the sentinels of the TOML codec,
// internal/service/data/codec/toml — range 0.3.5.* (ADR 0005
// service/data/codec/toml block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
package toml

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.5.0 - 0.3.5.255

// CodeTOMLMarshalFailed identifies a Go value the encoder cannot write as TOML.
const CodeTOMLMarshalFailed errs.Code = 0x00_03_05_01 // 0.3.5.1

// CodeTOMLUnmarshalFailed identifies a document the decoder cannot read, or a
// value its target cannot hold.
const CodeTOMLUnmarshalFailed errs.Code = 0x00_03_05_02 // 0.3.5.2
