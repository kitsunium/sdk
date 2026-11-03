// Package jcs writes the JSON Canonicalization Scheme of RFC 8785: one byte
// form for a JSON document, whatever the order of its members or the spelling
// of its numbers and strings, so it can be signed or hashed and checked by
// anyone who canonicalizes the same document.
//
// # The canonical form
//
// The document must be one I-JSON value (RFC 7493): valid UTF-8, no
// duplicated member name, no trailing data. It is then written with no
// whitespace; object members sorted by their names' UTF-16 code units
// (§3.2.3), recursively; strings with only '"', '\\' and the control
// characters escaped — \b, \t, \n, \f and \r by name, the others as \u00XX
// in lower case — and every other character as itself (§3.2.2.2); numbers as
// ECMAScript writes an IEEE 754 double (§3.2.2.3): the shortest digits that
// read back as the same double, in decimal notation from 1e-6 up to 1e21 and
// in exponent notation outside, -0 as 0. A number past the largest finite
// double is refused.
//
// It is not a codec Format (ADR 0102's precedent): a canonical form is a
// policy over JSON, not a wire format of its own. Nothing it returns in an
// error comes from the document.
package jcs
