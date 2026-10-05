//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/data/codec/form .

// Package form registers the application/x-www-form-urlencoded codec with the
// SDK's codec registry — and no other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/form"
//
// The codec reads and writes the encoding every HTML form posts. Its native
// value is url.Values — map[string][]string and map[string]string are
// accepted too — because a repeated key is the only array syntax the format
// has: a body repeating a key decoded into a map[string]string is refused
// rather than reduced to one value. Through the codec package's Marshal any
// other value is carried as one JSON-mediated field.
//
// Everything that dispatches through the registry by format name then
// reads and writes it: the codec package's Marshal and Unmarshal,
// config.FSSource and i18n.LoadFS. Importing
// github.com/kitsunium/sdk/pkg/v1/data/codec instead registers every
// format the SDK ships; this package links this one codec, the core
// package declaring its codes and the standard library, and nothing else.
// Importing both is harmless: a format is registered by the package that
// implements it, which Go initialises once however many packages import it.
//
// # Errors
//
// Failures carry the range 0.3.40.*; match them with errs.HasCode, or the
// sentinels with errors.Is:
//
//	CodeValueInvalid     0.3.40.1  the value or the target is not url.Values-shaped
//	CodeUnmarshalFailed  0.3.40.2  a malformed escape, a ';' separator, or a decode bound exceeded
//	CodeMultiValue       0.3.40.3  a repeated key decoded into a map[string]string
//
// There is no marshal failure: past the shape check, percent-escaping a map of
// strings cannot fail.
//
// No error message quotes the input.
package form

import (
	// The implementation registers itself with the codec registry as it is
	// initialised; importing it is what registers the format.
	_ "github.com/kitsunium/sdk/internal/service/data/codec/form"
)

// Format is the name the urlencoded form is registered under. It is an untyped
// constant, so it goes wherever a format name is taken — the codec package's
// Marshal, config.FSSource's string, i18n.LoadFS's codec.Format — without a
// conversion.
const Format = "form"
