// Package toml — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
package toml

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// MarshalFailed reports a Go value TOML cannot represent: not a map or a
	// struct at the root, a nil interface, a channel, a function or a complex
	// number, an unsigned integer above the largest int64, a string that is
	// not UTF-8, a cycle — or a writer or a MarshalText method that failed.
	MarshalFailed = errs.Define(CodeTOMLMarshalFailed, "MARSHAL_FAILED",
		"TOML encoding failed",
		"service/data/codec/toml: the value has no TOML representation; the problem and type fields say why")

	// UnmarshalFailed reports a document that is not TOML, a document past
	// the size or nesting bound, or a value its target cannot hold.
	UnmarshalFailed = errs.Define(CodeTOMLUnmarshalFailed, "UNMARSHAL_FAILED",
		"TOML decoding failed",
		"service/data/codec/toml: the document is not valid TOML, or a value does not fit the target; the problem, line and column fields locate it")
)
