// Package jsonschema validates JSON documents somebody else wrote against a
// JSON Schema 2020-12 they publish — a webhook's payload, an OpenAPI request
// body, another program's configuration (#882, ADR 0210) — with the standard
// library alone.
//
// NewValidator compiles the core, applicator and validation vocabularies:
// "$ref" within the document and to the resources Config registers by URI —
// a remote reference is refused, never fetched —, "$id", "$anchor", "$defs",
// the combinators, the conditionals, the array and object applicators, and
// every validation keyword, numbers compared exactly. "format" is an
// annotation unless Config.AssertFormats names it (date-time, uuid, email).
// Out of scope, each refused by name: drafts before 2020-12, "$dynamicRef"
// and "$dynamicAnchor", "$vocabulary", the unevaluated and the content
// vocabularies; a "pattern" Go's regexp does not read is refused when the
// schema is compiled.
//
// Validate reads a document through strictjson's bounds — a byte ceiling,
// one value, no duplicated member name — and returns a
// validation.ReportValue: each violation located in the validation
// package's path grammar ("items[2].name"), named by its keyword, and never
// quoting the document. A compiled Validator is immutable and reused.
package jsonschema
