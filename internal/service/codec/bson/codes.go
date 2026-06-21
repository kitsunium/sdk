// Package bson — range 0.3.36.* (ADR 0021 service/codec/bson block).
package bson

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.36.0 - 0.3.36.255

// CodeBSONMarshalFailed identifies a failure inside go.mongodb.org/mongo-driver/bson.Marshal
// (e.g. a top-level non-document value, which BSON cannot represent).
const CodeBSONMarshalFailed errs.Code = 0x00_03_24_01 // 0.3.36.1

// CodeBSONUnmarshalFailed identifies a failure inside go.mongodb.org/mongo-driver/bson.Unmarshal.
const CodeBSONUnmarshalFailed errs.Code = 0x00_03_24_02 // 0.3.36.2

// CodeBSONSizeExceeded identifies an Unmarshal input whose length exceeds the
// 10 MiB hard cap (CWE-400 memory-exhaustion defence before the decoder runs).
const CodeBSONSizeExceeded errs.Code = 0x00_03_24_03 // 0.3.36.3
