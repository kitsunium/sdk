// Package msgpack — declares the sentinel *errs.Error values for MessagePack.
package msgpack

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure from vmihailenco/msgpack/v5.Marshal.
	MarshalFailed = errs.Define(CodeMsgPackMarshalFailed, "MARSHAL_FAILED",
		"MessagePack encoding failed",
		"service/codec/msgpack: github.com/vmihailenco/msgpack/v5.Marshal returned an error")

	// UnmarshalFailed wraps a failure from vmihailenco/msgpack/v5.Unmarshal.
	UnmarshalFailed = errs.Define(CodeMsgPackUnmarshalFailed, "UNMARSHAL_FAILED",
		"MessagePack decoding failed",
		"service/codec/msgpack: github.com/vmihailenco/msgpack/v5.Unmarshal returned an error")
)
