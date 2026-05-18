// Package pem — range 0.3.10.* (ADR 0005 service/codec/pem block).
package pem

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.10.0 - 0.3.10.255

// CodePEMMarshalFailed identifies a failure inside encoding/pem.Encode.
const CodePEMMarshalFailed errs.Code = 0x00_03_0A_01 // 0.3.10.1

// CodePEMUnmarshalFailed identifies a failure inside encoding/pem.Decode
// (no PEM block found in input).
const CodePEMUnmarshalFailed errs.Code = 0x00_03_0A_02 // 0.3.10.2

// CodePEMValueInvalid identifies a Marshal call that did not receive a *pem.Block
// or an Unmarshal call whose target is not a **pem.Block.
const CodePEMValueInvalid errs.Code = 0x00_03_0A_03 // 0.3.10.3
