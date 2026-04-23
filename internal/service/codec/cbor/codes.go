// Package cbor: codes.go — range 0.3.6.* (ADR 0005 service/codec/cbor block).
package cbor

// range: 0.3.6.0 - 0.3.6.255

// CodeCBORMarshalFailed identifies a failure inside fxamacker/cbor/v2.Marshal.
const CodeCBORMarshalFailed = 0x00_03_06_01 // 0.3.6.1

// CodeCBORUnmarshalFailed identifies a failure inside fxamacker/cbor/v2.Unmarshal.
const CodeCBORUnmarshalFailed = 0x00_03_06_02 // 0.3.6.2
