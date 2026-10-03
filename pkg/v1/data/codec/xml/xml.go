//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/data/codec/xml .

// Package xml registers the XML codec with the SDK's codec registry — and no
// other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/xml"
//
// The codec is the standard library's encoding/xml, with its struct tags and
// its rules: a value marshals as an element, a struct names it with an
// XMLName field or its type name. It streams, one element per Encode or
// Decode.
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
// Failures carry the range 0.3.3.*; match them with errs.HasCode, or the
// sentinels with errors.Is:
//
//	CodeMarshalFailed    0.3.3.1  encoding/xml could not encode the value
//	CodeUnmarshalFailed  0.3.3.2  the input is not XML the target can hold
//
// No error message quotes the input.
package xml

import (
	corexml "github.com/kitsunium/sdk/internal/core/data/codec/xml"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	// The implementation registers itself with the codec registry as it is
	// initialised; importing it is what registers the format.
	_ "github.com/kitsunium/sdk/internal/service/data/codec/xml"
)

// Format is the name XML is registered under. It is an untyped constant, so it
// goes wherever a format name is taken — the codec package's Marshal,
// config.FSSource's string, i18n.LoadFS's codec.Format — without a conversion.
const Format = "xml"

// The error codes, range 0.3.3.*, declared in the core (ADR 0160).
const (
	// CodeMarshalFailed is 0.3.3.1: encoding/xml could not encode the value.
	CodeMarshalFailed errs.Code = corexml.CodeXMLMarshalFailed
	// CodeUnmarshalFailed is 0.3.3.2: the input is not XML the target can
	// hold.
	CodeUnmarshalFailed errs.Code = corexml.CodeXMLUnmarshalFailed
)

// The sentinels, for errors.Is: each carries the code of the same name.
var (
	// MarshalFailed is the sentinel of [CodeMarshalFailed].
	MarshalFailed = corexml.MarshalFailed
	// UnmarshalFailed is the sentinel of [CodeUnmarshalFailed].
	UnmarshalFailed = corexml.UnmarshalFailed
)
