// Package toml — declares the sentinel *errs.Error values for TOML.
//
// Every refusal is one of the two below. What went wrong travels in fields —
// problem, a fixed sentence; line and column; the dotted key and the Go type
// for a decode that does not fit its target — and never as a byte of the
// document or of the value, which may be a secret.
package toml

import "github.com/kitsunium/sdk/internal/kernel/errs"

// The private messages of a refusal that wraps a cause — a writer, a reader,
// a MarshalText or UnmarshalText method — under one of the two codes. Its
// reason and public message are the sentinel's own.
const (
	// privateTextMarshalFailed is the private message of a TextMarshaler's
	// refusal.
	privateTextMarshalFailed string = "service/codec/toml: a MarshalText method returned an error; it is the cause"
	// privateTextRefused is the private message of a TextUnmarshaler's
	// refusal.
	privateTextRefused string = "service/codec/toml: an UnmarshalText method refused a value; its error is the cause"
	// privateWriteFailed is the private message of a writer's refusal.
	privateWriteFailed string = "service/codec/toml: the writer refused the encoded document; its error is the cause"
	// privateReadFailed is the private message of a reader's failure.
	privateReadFailed string = "service/codec/toml: the reader failed before the document ended; its error is the cause"
)

var (
	// MarshalFailed reports a Go value TOML cannot represent: not a map or a
	// struct at the root, a nil interface, a channel, a function or a complex
	// number, an unsigned integer above the largest int64, a string that is
	// not UTF-8, a cycle — or a writer or a MarshalText method that failed.
	MarshalFailed = errs.Define(CodeTOMLMarshalFailed, "MARSHAL_FAILED",
		"TOML encoding failed",
		"service/codec/toml: the value has no TOML representation; the problem and type fields say why")

	// UnmarshalFailed reports a document that is not TOML, a document past
	// the size or nesting bound, or a value its target cannot hold.
	UnmarshalFailed = errs.Define(CodeTOMLUnmarshalFailed, "UNMARSHAL_FAILED",
		"TOML decoding failed",
		"service/codec/toml: the document is not valid TOML, or a value does not fit the target; the problem, line and column fields locate it")
)
