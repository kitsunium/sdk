// Package jcs writes the JSON Canonicalization Scheme of RFC 8785: one byte
// form for a JSON document — members sorted, no whitespace, numbers and
// strings spelled one way — so it can be signed or hashed, and checked by
// anyone who canonicalizes the same document.
//
//	canonical, err := jcs.Canonicalize(document)
//	digest := sha256.Sum256(canonical)
//
// Marshal canonicalizes a Go value's encoding/json/v2 encoding. The document
// must be one I-JSON value (RFC 7493) — valid UTF-8, no duplicated member
// name — or Canonicalize returns [NotJSON], naming the offset and never a byte
// of the document; a number past the largest finite double is
// [NumberInvalid]. It is not a codec Format: a canonical form is a policy over
// JSON, as strictjson is a policy over decoding.
package jcs
