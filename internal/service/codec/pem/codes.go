// Package pem: codes.go — range 3285-3289 for PEM codec errors.
package pem

// range: 3285-3289

// CodeMarshalFailed identifies a failure inside encoding/pem.Encode.
const CodeMarshalFailed int = 3286

// CodeUnmarshalFailed identifies a failure inside encoding/pem.Decode
// (no PEM block found in input).
const CodeUnmarshalFailed int = 3287

// CodeValueInvalid identifies a Marshal call that did not receive a *pem.Block
// or an Unmarshal call whose target is not a **pem.Block.
const CodeValueInvalid int = 3288
