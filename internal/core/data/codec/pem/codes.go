// Package pem declares the error codes and the sentinels of the PEM codec,
// internal/service/data/codec/pem — range 0.3.10.* (ADR 0005
// service/data/codec/pem block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
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
