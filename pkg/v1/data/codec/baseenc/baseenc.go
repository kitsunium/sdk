//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/data/codec/baseenc .

// Package baseenc registers the base-N codec with the SDK's codec
// registry — and no other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/baseenc"
//
// It registers nine Formats, one per alphabet, all through one pipeline: a
// value is encoded as JSON and the JSON text as base-N, and decoding runs the
// two steps backwards — so every alphabet carries any Go value encoding/json
// can, and swapping one alphabet for another is a change of Format name.
// Every Format streams.
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
// Failures carry the range 0.3.24.*; match them with errs.HasCode, or the
// sentinels with errors.Is:
//
//	CodeMarshalFailed    0.3.24.1  the value has no JSON encoding
//	CodeUnmarshalFailed  0.3.24.2  the decoded text is not JSON the target can hold
//	CodeDecodeFailed     0.3.24.3  the input is not text of the Format's alphabet
//	CodeSizeExceeded     0.3.24.4  an input past its bound: 10 MiB, 4 KiB for base58 and base62
//
// No error message quotes the input.
package baseenc

import (
	corebaseenc "github.com/kitsunium/sdk/internal/core/data/codec/baseenc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	// The implementation registers itself with the codec registry as it is
	// initialised; importing it is what registers the format.
	_ "github.com/kitsunium/sdk/internal/service/data/codec/baseenc"
)

// The nine Formats this package registers. Each is an untyped constant, so it
// goes wherever a format name is taken — the codec package's Marshal,
// config.FSSource's string, i18n.LoadFS's codec.Format — without a
// conversion.
const (
	// Base64 is standard base64, RFC 4648 §4.
	Base64 = "base64"
	// Base64URL is the URL- and filename-safe base64 alphabet, RFC 4648 §5.
	Base64URL = "base64url"
	// Base32 is standard base32, RFC 4648 §6.
	Base32 = "base32"
	// Base16 is upper-case hexadecimal, RFC 4648 §8.
	Base16 = "base16"
	// Hex is lower-case hexadecimal.
	Hex = "hex"
	// ASCII85 is Adobe's Ascii85, four bytes in five characters.
	ASCII85 = "ascii85"
	// Base45 is RFC 9285's alphabet, the one a QR code's alphanumeric mode holds.
	Base45 = "base45"
	// Base58 is Bitcoin's alphabet; its base conversion is quadratic, so its input is capped at 4 KiB.
	Base58 = "base58"
	// Base62 is the alphanumeric alphabet; capped at 4 KiB like Base58.
	Base62 = "base62"
)

// The error codes, range 0.3.24.*, declared in the core (ADR 0160).
const (
	// CodeMarshalFailed identifies the value has no JSON encoding (0.3.24.1).
	CodeMarshalFailed errs.Code = corebaseenc.CodeBaseEncMarshalFailed
	// CodeUnmarshalFailed identifies the decoded text is not JSON the target can hold (0.3.24.2).
	CodeUnmarshalFailed errs.Code = corebaseenc.CodeBaseEncUnmarshalFailed
	// CodeDecodeFailed identifies the input is not text of the Format's alphabet (0.3.24.3).
	CodeDecodeFailed errs.Code = corebaseenc.CodeBaseEncDecodeFailed
	// CodeSizeExceeded identifies an input past its bound: 10 MiB, 4 KiB for base58 and base62 (0.3.24.4).
	CodeSizeExceeded errs.Code = corebaseenc.CodeBaseEncSizeExceeded
)

// The sentinels, for errors.Is: each carries the code of the same name.
var (
	// MarshalFailed is the sentinel of [CodeMarshalFailed].
	MarshalFailed = corebaseenc.BaseEncMarshalFailed
	// UnmarshalFailed is the sentinel of [CodeUnmarshalFailed].
	UnmarshalFailed = corebaseenc.BaseEncUnmarshalFailed
	// DecodeFailed is the sentinel of [CodeDecodeFailed].
	DecodeFailed = corebaseenc.BaseEncDecodeFailed
	// SizeExceeded is the sentinel of [CodeSizeExceeded].
	SizeExceeded = corebaseenc.BaseEncSizeExceeded
)
