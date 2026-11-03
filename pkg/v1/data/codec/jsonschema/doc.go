// Package jsonschema validates JSON documents somebody else wrote against a
// JSON Schema 2020-12 they publish — a webhook's payload, an OpenAPI request
// body, another program's configuration — with the standard library alone.
//
//	v, err := jsonschema.NewValidator(schema, jsonschema.Config{})
//	report, err := v.Validate(body, 1<<20)
//	if !report.OK() { … report.Paths() … }
//
// The core, applicator and validation vocabularies are compiled; "$ref"
// resolves within the schema and to the resources Config registers, never
// over the network; "format" is asserted only for the formats
// Config.AssertFormats names. Drafts before 2020-12, the dynamic scope,
// "$vocabulary", the unevaluated and content vocabularies are refused by
// name. A report's paths follow the validation package's grammar and no
// message quotes the document.
package jsonschema
