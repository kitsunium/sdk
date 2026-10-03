//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/data/codec/csv .

// Package csv registers the CSV codec with the SDK's codec registry — and no
// other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/csv"
//
// The codec is the standard library's encoding/csv over RFC 4180 records: its
// native value is a [][]string, the rows of a table. Through the codec
// package's Marshal any other value is carried as its JSON, in the one cell
// of a one-column table, and read back the same way.
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
// Failures carry the range 0.3.8.*; match them with errs.HasCode, or the
// sentinels with errors.Is:
//
//	CodeMarshalFailed    0.3.8.1  encoding/csv refused to write the records
//	CodeUnmarshalFailed  0.3.8.2  the input is not CSV encoding/csv reads
//	CodeValueInvalid     0.3.8.3  the value or the target is not a [][]string
//
// No error message quotes the input.
package csv

import (
	corecsv "github.com/kitsunium/sdk/internal/core/data/codec/csv"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	// The implementation registers itself with the codec registry as it is
	// initialised; importing it is what registers the format.
	_ "github.com/kitsunium/sdk/internal/service/data/codec/csv"
)

// Format is the name CSV is registered under. It is an untyped constant, so it
// goes wherever a format name is taken — the codec package's Marshal,
// config.FSSource's string, i18n.LoadFS's codec.Format — without a conversion.
const Format = "csv"

// The error codes, range 0.3.8.*, declared in the core (ADR 0160).
const (
	// CodeMarshalFailed is 0.3.8.1: encoding/csv refused to write the records.
	CodeMarshalFailed errs.Code = corecsv.CodeCSVMarshalFailed
	// CodeUnmarshalFailed is 0.3.8.2: the input is not CSV encoding/csv reads.
	CodeUnmarshalFailed errs.Code = corecsv.CodeCSVUnmarshalFailed
	// CodeValueInvalid is 0.3.8.3: the value or the target is not a
	// [][]string.
	CodeValueInvalid errs.Code = corecsv.CodeCSVValueInvalid
)

// The sentinels, for errors.Is: each carries the code of the same name.
var (
	// MarshalFailed is the sentinel of [CodeMarshalFailed].
	MarshalFailed = corecsv.MarshalFailed
	// UnmarshalFailed is the sentinel of [CodeUnmarshalFailed].
	UnmarshalFailed = corecsv.UnmarshalFailed
	// ValueInvalid is the sentinel of [CodeValueInvalid].
	ValueInvalid = corecsv.ValueInvalid
)
