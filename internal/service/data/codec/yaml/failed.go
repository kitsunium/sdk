// Package yaml — how a refusal is built: the position every decoding refusal
// carries, a syntax error at it, a construct refused by name, and an encoding
// failure. The codes (range 0.3.4.*) and the sentinels are declared in
// internal/core/data/codec/yaml (ADR 0160).
package yaml

import (
	coreyaml "github.com/kitsunium/sdk/internal/core/data/codec/yaml"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// at returns the line and column fields every decoding refusal carries: both
// 1-based, so they read as an editor shows them.
func at(line, column int) []errs.FieldValue {
	//: the position, never the text found there.
	return []errs.FieldValue{errs.Int("line", line), errs.Int("column", column)}
}

// syntaxError is UnmarshalFailed at a position, with a detail that names what
// is wrong and never quotes the input.
func syntaxError(line, column int, detail string) error {
	//: the sentinel is the origin, so its code and public text win.
	return errs.Wrap(coreyaml.UnmarshalFailed, errs.WrapParams{}, append(at(line, column), errs.String("detail", detail))...)
}

// refused is a construct the subset names and refuses, at a position. The
// refusal's own code is the origin and UnmarshalFailed joins its trail, so a
// caller testing for "any decoding failure" by code still matches.
func refused(sentinel *errs.Error, line, column int) error {
	//: the construct's sentinel, then the decode failure it is.
	return errs.Wrap(sentinel, errs.WrapParams{Code: coreyaml.CodeYAMLUnmarshalFailed}, at(line, column)...)
}

// marshalError is MarshalFailed with a detail and, when known, the Go type
// that could not be written.
func marshalError(detail, goType string) error {
	//: a type name describes the program, never the data.
	if goType == "" {
		//: no type to name.
		return errs.Wrap(coreyaml.MarshalFailed, errs.WrapParams{}, errs.String("detail", detail))
	}
	//: the detail and the type.
	return errs.Wrap(coreyaml.MarshalFailed, errs.WrapParams{}, errs.String("detail", detail), errs.String("type", goType))
}
