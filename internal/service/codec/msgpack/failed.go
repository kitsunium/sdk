// Package msgpack — range 0.3.7.* (ADR 0005 service/codec/msgpack block).
// The two codes predate the native implementation and keep their values: every
// encode failure is MARSHAL_FAILED and every decode failure UNMARSHAL_FAILED,
// whatever the cause, so a caller routing on them is unaffected by the change
// of engine.
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

var (
	// MarshalFailed is the sentinel of an encode failure.
	MarshalFailed = errs.Define(CodeMsgPackMarshalFailed, "MARSHAL_FAILED",
		"MessagePack encoding failed",
		"service/codec/msgpack: Marshal/Append/Encode failed")

	// UnmarshalFailed is the sentinel of a decode failure.
	UnmarshalFailed = errs.Define(CodeMsgPackUnmarshalFailed, "UNMARSHAL_FAILED",
		"MessagePack decoding failed",
		"service/codec/msgpack: Unmarshal/Decode failed")
)
