// Package pem: codes.go — range 3285-3289 for PEM codec errors.
package pem

// range: 3285-3289

// CodePEMMarshalFailed identifies a failure inside encoding/pem.Encode.
const CodePEMMarshalFailed int = 3286

// CodePEMUnmarshalFailed identifies a failure inside encoding/pem.Decode
// (no PEM block found in input).
const CodePEMUnmarshalFailed int = 3287

// CodePEMValueInvalid identifies a Marshal call that did not receive a *pem.Block
// or an Unmarshal call whose target is not a **pem.Block.
const CodePEMValueInvalid int = 3288
