// Package asn1 registers the ASN.1 DER codec with the SDK's codec registry —
// and no other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/asn1"
//
// The codec is the standard library's encoding/asn1: Marshal writes DER — the
// only encoding that package produces — and Unmarshal also accepts BER. The
// value is whatever encoding/asn1 maps: a struct of exported fields, an
// integer, a string, a time, a []byte, an asn1.ObjectIdentifier. Its MIME
// type is application/pkix-cert and its extensions .der and .cer.
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
// Failures carry the range 0.3.9.*; match them with errs.HasCode, or the
// sentinels with errors.Is:
//
//	CodeMarshalFailed    0.3.9.1  encoding/asn1 could not encode the value
//	CodeUnmarshalFailed  0.3.9.2  the input is not DER or BER the target can hold
//
// No error message quotes the input.
package asn1
