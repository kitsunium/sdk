// Package ndjson: codes.go — range 3300-3309 for NDJSON codec errors.
package ndjson

// range: 3300-3309

// CodeNDJSONMarshalFailed identifies a failure inside encoding/json.Marshal
// when encoding a single NDJSON record.
const CodeNDJSONMarshalFailed int = 3301

// CodeNDJSONUnmarshalFailed identifies a failure inside encoding/json.Unmarshal
// when decoding a single NDJSON record.
const CodeNDJSONUnmarshalFailed int = 3302

// CodeNDJSONValueInvalid identifies a Marshal / Unmarshal call whose target is
// not a slice (NDJSON models a stream of records).
const CodeNDJSONValueInvalid int = 3303
