// Package ndjson: codes.go — range 3300-3309 for NDJSON codec errors.
package ndjson

// range: 3300-3309

// CodeMarshalFailed identifies a failure inside encoding/json.Marshal
// when encoding a single NDJSON record.
const CodeMarshalFailed int = 3301

// CodeUnmarshalFailed identifies a failure inside encoding/json.Unmarshal
// when decoding a single NDJSON record.
const CodeUnmarshalFailed int = 3302

// CodeValueInvalid identifies a Marshal / Unmarshal call whose target is
// not a slice (NDJSON models a stream of records).
const CodeValueInvalid int = 3303
