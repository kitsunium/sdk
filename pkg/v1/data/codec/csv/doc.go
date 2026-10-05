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
