// Package toml: codes.go — range 0.3.5.* (ADR 0005 service/codec/toml block).
package toml

// range: 0.3.5.0 - 0.3.5.255

// CodeTOMLMarshalFailed identifies a failure inside pelletier/go-toml/v2.Marshal.
const CodeTOMLMarshalFailed = 0x00_03_05_01 // 0.3.5.1

// CodeTOMLUnmarshalFailed identifies a failure inside pelletier/go-toml/v2.Unmarshal.
const CodeTOMLUnmarshalFailed = 0x00_03_05_02 // 0.3.5.2
