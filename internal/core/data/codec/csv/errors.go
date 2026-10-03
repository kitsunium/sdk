// Package csv — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
package csv

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed wraps a failure from encoding/csv.Writer.Write(All).
	MarshalFailed = errs.Define(CodeCSVMarshalFailed, "MARSHAL_FAILED",
		"CSV encoding failed",
		"service/data/codec/csv: encoding/csv.Writer.WriteAll returned an error")

	// UnmarshalFailed wraps a failure from encoding/csv.Reader.ReadAll.
	UnmarshalFailed = errs.Define(CodeCSVUnmarshalFailed, "UNMARSHAL_FAILED",
		"CSV decoding failed",
		"service/data/codec/csv: encoding/csv.Reader.ReadAll returned an error")

	// ValueInvalid fires when the caller does not pass a *[][]string.
	ValueInvalid = errs.Define(CodeCSVValueInvalid, "VALUE_INVALID",
		"CSV codec requires a [][]string value",
		"service/data/codec/csv: Marshal/Unmarshal called with non-[][]string argument")
)
