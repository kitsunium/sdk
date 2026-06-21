// Package bson — declares the sentinel *errs.Error values for BSON.
package bson

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure from go.mongodb.org/mongo-driver/bson.Marshal.
	MarshalFailed = errs.Define(CodeBSONMarshalFailed, "BSON_MARSHAL_FAILED",
		"BSON encoding failed",
		"service/codec/bson: go.mongodb.org/mongo-driver/bson.Marshal returned an error")

	// UnmarshalFailed wraps a failure from go.mongodb.org/mongo-driver/bson.Unmarshal.
	UnmarshalFailed = errs.Define(CodeBSONUnmarshalFailed, "BSON_UNMARSHAL_FAILED",
		"BSON decoding failed",
		"service/codec/bson: go.mongodb.org/mongo-driver/bson.Unmarshal returned an error")

	// SizeExceeded marks an Unmarshal input over the 10 MiB hard cap.
	SizeExceeded = errs.Define(CodeBSONSizeExceeded, "BSON_SIZE_EXCEEDED",
		"BSON input exceeds size limit",
		"service/codec/bson: len(data) exceeds maxBSONBytes")
)
