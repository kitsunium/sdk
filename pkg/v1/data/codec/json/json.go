//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/data/codec/json .

// Package json registers the JSON codec with the SDK's codec registry — and
// no other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/json"
//
// Everything that dispatches through the registry by format name then reads
// and writes JSON: config.FileSource and config.FSSource, i18n.LoadFS, and
// the codec package's Marshal and Unmarshal. Importing
// github.com/kitsunium/sdk/pkg/v1/data/codec instead registers every format the SDK
// ships — BSON, CBOR, MessagePack and the rest — which
// a program that only reads JSON does not need to link. This package links
// the JSON codec, the core package declaring its codes and the standard
// library's encoding/json, and nothing else.
//
// Importing both packages is harmless: a format is registered by the package
// that implements it, which Go initialises once however many packages import
// it.
//
// # Errors
//
// Failures carry the range 0.3.2.*; match them with errs.HasCode, or the
// sentinels with errors.Is:
//
//	CodeMarshalFailed    0.3.2.1  encoding/json could not encode the value
//	CodeUnmarshalFailed  0.3.2.2  the input is not JSON the target can hold
package json

import (
	corejson "github.com/kitsunium/sdk/internal/core/data/codec/json"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	// The implementation registers itself with the codec registry as it is
	// initialised; importing it is what registers the format.
	_ "github.com/kitsunium/sdk/internal/service/data/codec/json"
)

// Format is the name JSON is registered under. It is an untyped constant, so
// it goes wherever a format name is taken — config.FSSource's string,
// i18n.LoadFS's codec.Format — without a conversion.
const Format = "json"

// The error codes, range 0.3.2.*, declared in the core (ADR 0160).
const (
	// CodeMarshalFailed identifies a value encoding/json could not encode (0.3.2.1).
	CodeMarshalFailed errs.Code = corejson.CodeJSONMarshalFailed
	// CodeUnmarshalFailed identifies input that is not JSON the target can hold (0.3.2.2).
	CodeUnmarshalFailed errs.Code = corejson.CodeJSONUnmarshalFailed
)

// The sentinels, for errors.Is: each carries the code of the same name.
var (
	// MarshalFailed is the sentinel of [CodeMarshalFailed].
	MarshalFailed = corejson.MarshalFailed
	// UnmarshalFailed is the sentinel of [CodeUnmarshalFailed].
	UnmarshalFailed = corejson.UnmarshalFailed
)
