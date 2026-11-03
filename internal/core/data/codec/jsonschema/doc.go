// Package jsonschema declares the error codes and the sentinels of the JSON
// Schema 2020-12 validator, internal/service/data/codec/jsonschema — range
// 0.3.103.* (ADR 0210).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2). Nothing here compiles or validates.
//
// Package jsonschema — the sentinel *errs.Error values, one per code. No
// Public and no Private carries a byte of a schema or a document; the fields
// name a keyword, a reference or a format, which the schema's author wrote.
package jsonschema
