// Package protobuf — range 0.3.38.* (ADR 0023 third-party/codec/protobuf block).
package protobuf

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.38.0 - 0.3.38.255

// CodeProtobufMarshalFailed identifies a Marshal failure — either the value is
// not a proto.Message (Protobuf is schema-bound) or google.golang.org/protobuf
// /proto.Marshal returned an error.
const CodeProtobufMarshalFailed errs.Code = 0x00_03_26_01 // 0.3.38.1

// CodeProtobufUnmarshalFailed identifies an Unmarshal failure — the target is
// not a proto.Message, or proto.Unmarshal rejected the wire bytes.
const CodeProtobufUnmarshalFailed errs.Code = 0x00_03_26_02 // 0.3.38.2

// CodeProtobufSizeExceeded identifies an Unmarshal input over the 10 MiB cap
// (CWE-400 defence before the decoder allocates).
const CodeProtobufSizeExceeded errs.Code = 0x00_03_26_03 // 0.3.38.3
