// Package cbor: codes.go — range 0.3.6.* (ADR 0005 service/codec/cbor block).
package cbor

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.6.0 - 0.3.6.255

// CodeCBORMarshalFailed identifies a failure inside fxamacker/cbor/v2.Marshal.
const CodeCBORMarshalFailed errs.Code = 0x00_03_06_01 // 0.3.6.1

// CodeCBORUnmarshalFailed identifies a failure inside fxamacker/cbor/v2.Unmarshal.
const CodeCBORUnmarshalFailed errs.Code = 0x00_03_06_02 // 0.3.6.2
