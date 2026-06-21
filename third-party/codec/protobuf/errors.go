// Package protobuf — declares the sentinel *errs.Error values for Protobuf.
package protobuf

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a non-proto.Message value or a proto.Marshal failure.
	MarshalFailed = errs.Define(CodeProtobufMarshalFailed, "PROTOBUF_MARSHAL_FAILED",
		"Protobuf encoding failed",
		"third-party/codec/protobuf: value is not a proto.Message or proto.Marshal returned an error")

	// UnmarshalFailed wraps a non-proto.Message target or a proto.Unmarshal failure.
	UnmarshalFailed = errs.Define(CodeProtobufUnmarshalFailed, "PROTOBUF_UNMARSHAL_FAILED",
		"Protobuf decoding failed",
		"third-party/codec/protobuf: target is not a proto.Message or proto.Unmarshal returned an error")

	// SizeExceeded marks an Unmarshal input over the 10 MiB hard cap.
	SizeExceeded = errs.Define(CodeProtobufSizeExceeded, "PROTOBUF_SIZE_EXCEEDED",
		"Protobuf input exceeds size limit",
		"third-party/codec/protobuf: len(data) exceeds maxProtobufBytes")
)
