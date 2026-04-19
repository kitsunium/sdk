// Package ndjson: errors.go declares the sentinel *errs.Error values for NDJSON.
package ndjson

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure from encoding/json.Marshal on a single record.
	MarshalFailed = errs.Define(CodeMarshalFailed, "MARSHAL_FAILED",
		"NDJSON encoding failed",
		"service/codec/ndjson: encoding/json.Marshal returned an error on a record")

	// UnmarshalFailed wraps a failure from encoding/json.Unmarshal on a single record.
	UnmarshalFailed = errs.Define(CodeUnmarshalFailed, "UNMARSHAL_FAILED",
		"NDJSON decoding failed",
		"service/codec/ndjson: encoding/json.Unmarshal returned an error on a record")

	// ValueInvalid fires when the caller does not pass a slice target.
	ValueInvalid = errs.Define(CodeValueInvalid, "VALUE_INVALID",
		"NDJSON codec requires a slice target",
		"service/codec/ndjson: Marshal/Unmarshal called with a non-slice argument")
)
