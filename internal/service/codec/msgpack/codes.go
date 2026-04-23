// Package msgpack: codes.go — range 0.3.7.* (ADR 0005 service/codec/msgpack block).
package msgpack

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.7.0 - 0.3.7.255

// CodeMsgPackMarshalFailed identifies a failure inside vmihailenco/msgpack/v5.Marshal.
const CodeMsgPackMarshalFailed errs.Code = 0x00_03_07_01 // 0.3.7.1

// CodeMsgPackUnmarshalFailed identifies a failure inside vmihailenco/msgpack/v5.Unmarshal.
const CodeMsgPackUnmarshalFailed errs.Code = 0x00_03_07_02 // 0.3.7.2
