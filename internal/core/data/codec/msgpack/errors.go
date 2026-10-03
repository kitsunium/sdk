// Package msgpack — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
package msgpack

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed is the sentinel of an encode failure.
	MarshalFailed = errs.Define(CodeMsgPackMarshalFailed, "MARSHAL_FAILED",
		"MessagePack encoding failed",
		"service/data/codec/msgpack: Marshal/Append/Encode failed")

	// UnmarshalFailed is the sentinel of a decode failure.
	UnmarshalFailed = errs.Define(CodeMsgPackUnmarshalFailed, "UNMARSHAL_FAILED",
		"MessagePack decoding failed",
		"service/data/codec/msgpack: Unmarshal/Decode failed")
)
