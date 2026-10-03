// Package msgpack declares the error codes and the sentinels of the
// MessagePack codec, internal/service/data/codec/msgpack — range 0.3.7.* (ADR
// 0005 service/data/codec/msgpack block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
package msgpack

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.7.0 - 0.3.7.255

// CodeMsgPackMarshalFailed identifies an encode failure: a value MessagePack
// cannot represent, a marshal method's error, a nesting past the depth limit,
// or a writer's error on the streaming path.
const CodeMsgPackMarshalFailed errs.Code = 0x00_03_07_01 // 0.3.7.1

// CodeMsgPackUnmarshalFailed identifies a decode failure: malformed or
// truncated input, an input over the size or depth limit, trailing bytes, or a
// value that does not fit its Go target.
const CodeMsgPackUnmarshalFailed errs.Code = 0x00_03_07_02 // 0.3.7.2
