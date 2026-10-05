// Package cbor registers the CBOR codec with the SDK's codec registry — and no
// other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/cbor"
//
// The codec is RFC 8949 written on the standard library: maps, arrays, the
// integers and floats, text and byte strings, tags 0 to 3 for times and big
// numbers, struct fields by their cbor tag or their json tag. Map pairs are
// written sorted by their encoded key, the deterministic encoding of RFC 8949
// §4.2.1, so one value always encodes to the same bytes. Input is checked
// whole — well-formed, valid, within its bounds — before the target is
// written. It streams.
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
// Failures carry the range 0.3.6.*; match them with errs.HasCode, or the
// sentinels with errors.Is:
//
//	CodeMarshalFailed    0.3.6.1  a value CBOR cannot carry, invalid UTF-8, nesting past the bound
//	CodeUnmarshalFailed  0.3.6.2  input that is not one well-formed item, or an item the target cannot hold
//
// No error message quotes the input.
package cbor
