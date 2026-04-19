// Package csv: errors.go declares the sentinel *errs.Error values for CSV.
package csv

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure from encoding/csv.Writer.Write(All).
	MarshalFailed = errs.Define(CodeMarshalFailed, "MARSHAL_FAILED",
		"CSV encoding failed",
		"service/codec/csv: encoding/csv.Writer.WriteAll returned an error")

	// UnmarshalFailed wraps a failure from encoding/csv.Reader.ReadAll.
	UnmarshalFailed = errs.Define(CodeUnmarshalFailed, "UNMARSHAL_FAILED",
		"CSV decoding failed",
		"service/codec/csv: encoding/csv.Reader.ReadAll returned an error")

	// ValueInvalid fires when the caller does not pass a *[][]string.
	ValueInvalid = errs.Define(CodeValueInvalid, "VALUE_INVALID",
		"CSV codec requires a [][]string value",
		"service/codec/csv: Marshal/Unmarshal called with non-[][]string argument")
)
