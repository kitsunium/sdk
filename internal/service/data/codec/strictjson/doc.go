// Package strictjson — the reader that bounds a document and remembers why
// it stopped.
//
// Package strictjson decodes exactly one JSON document into a Go value,
// refusing everything the permissive decoder lets through, within a byte
// bound, with errors that never quote the input.
//
// The universal codec's json format is encoding/json with its defaults: no
// size limit, unknown members silently dropped, a member matched to a field
// case-insensitively, a duplicated name resolved by whichever came last, and
// trailing data after the value ignored. Each of those is a way for one
// document to be read two ways — by this program and by whatever else parsed
// it first — and none of them fails. This package is the other decoder, built
// on encoding/json/v2 whose defaults already refuse most of them:
//
//   - a document longer than the bound is refused before it is buffered past
//     the bound;
//   - an object member the target does not declare is refused, and so is one
//     that differs from a declared name only by case;
//   - a duplicated member name, invalid UTF-8, and anything after the one
//     value are refused;
//   - a number out of the field's range is refused rather than wrapped.
//
// Every refusal is a typed error whose text is a fixed sentence: the
// document's bytes appear in no Public, no Private and no wrapped cause.
// Where the document failed is available through PointerOf, as a JSON
// Pointer built from the document's own member names — a location, never a
// value.
package strictjson
