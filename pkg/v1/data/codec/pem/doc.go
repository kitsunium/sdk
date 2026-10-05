// Package pem registers the PEM codec with the SDK's codec registry — and no
// other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/pem"
//
// The codec is the standard library's encoding/pem, RFC 7468's textual
// armour: its native value is a *pem.Block — a type, headers and the bytes —
// and an Unmarshal target is a **pem.Block. Through the codec package's
// Marshal any other value is carried as the bytes of one JSON-typed block.
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
// Failures carry the range 0.3.10.*; match them with errs.HasCode, or the
// sentinels with errors.Is:
//
//	CodeMarshalFailed    0.3.10.1  encoding/pem refused to write the block
//	CodeUnmarshalFailed  0.3.10.2  no PEM block in the input
//	CodeValueInvalid     0.3.10.3  the value is not a *pem.Block, or the target a **pem.Block
//
// No error message quotes the input.
package pem
