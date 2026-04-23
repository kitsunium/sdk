// Package pem: codes.go — range 0.3.10.* (ADR 0005 service/codec/pem block).
package pem

// range: 0.3.10.0 - 0.3.10.255

// CodePEMMarshalFailed identifies a failure inside encoding/pem.Encode.
const CodePEMMarshalFailed = 0x00_03_0A_01 // 0.3.10.1

// CodePEMUnmarshalFailed identifies a failure inside encoding/pem.Decode
// (no PEM block found in input).
const CodePEMUnmarshalFailed = 0x00_03_0A_02 // 0.3.10.2

// CodePEMValueInvalid identifies a Marshal call that did not receive a *pem.Block
// or an Unmarshal call whose target is not a **pem.Block.
const CodePEMValueInvalid = 0x00_03_0A_03 // 0.3.10.3
