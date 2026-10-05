// Package jsonpatch computes the structural difference between two JSON
// documents as RFC 6902 operations — add, remove and replace, each at an
// RFC 6901 JSON Pointer — with the value each operation writes AND the value
// it replaces or removes (ADR 0143). Public facade: pkg/v1/data/codec/jsonpatch.
//
// It is what a record's history needs to say what changed between two of its
// versions: the operations are the edits, both values are what a reader shows,
// and applied in order to the first document they give the second.
//
// # How documents are compared
//
// Each document must be exactly one JSON value, read strictly: no duplicated
// member name, valid UTF-8, no trailing data. Values are compared as RFC 6902
// §4.6 compares them — strings once unescaped, numbers numerically (1, 1.0 and
// 1e0 are one number, compared exactly, never through a float), arrays element
// by element, objects by member name whatever their order.
//
// # What the operations are
//
//   - Two objects: a member only the first has is removed, one only the second
//     has is added, and one both have is compared in turn. Members come in
//     byte order of their names, so the same two documents always give the
//     same operations.
//   - Two arrays: the elements both keep, in order, are found — the longest
//     common subsequence of the elements that differ, after the equal ones at
//     both ends — and between two kept elements, those removed and those added
//     are paired in order and compared in turn, the rest removed or added. An
//     insertion in the middle is one add, not a rewrite of every element after
//     it. Indices are those of the array as the operations before have left
//     it, as RFC 6902 applies them.
//   - Anything else that differs — another kind, another string, another number
//     — is replaced whole; two roots of different kinds are one replace at "".
//
// # What it is not
//
// It emits no move, copy or test, and does not apply a patch. A value is the
// JSON of that part of the document, compact; a number keeps its spelling.
// Nothing it returns in an error comes from a document.
//
// Package jsonpatch — a JSON document read into a tree, compared by value, and
// written back compact.
package jsonpatch
