// Package toml — the private messages of the refusals that wrap a cause.
//
// Every refusal is one of the two sentinels internal/core/data/codec/toml
// declares for this package (ADR 0160). What went wrong travels in fields —
// problem, a fixed sentence; line and column; the dotted key and the Go type
// for a decode that does not fit its target — and never as a byte of the
// document or of the value, which may be a secret.
package toml

// The private messages of a refusal that wraps a cause — a writer, a reader,
// a MarshalText or UnmarshalText method — under one of the two codes. Its
// reason and public message are the sentinel's own.
const (
	// privateTextMarshalFailed is the private message of a TextMarshaler's
	// refusal.
	privateTextMarshalFailed string = "service/data/codec/toml: a MarshalText method returned an error; it is the cause"
	// privateTextRefused is the private message of a TextUnmarshaler's
	// refusal.
	privateTextRefused string = "service/data/codec/toml: an UnmarshalText method refused a value; its error is the cause"
	// privateWriteFailed is the private message of a writer's refusal.
	privateWriteFailed string = "service/data/codec/toml: the writer refused the encoded document; its error is the cause"
	// privateReadFailed is the private message of a reader's failure.
	privateReadFailed string = "service/data/codec/toml: the reader failed before the document ended; its error is the cause"
)
