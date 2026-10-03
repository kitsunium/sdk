// Package json declares the error codes and the sentinels of the JSON codec,
// internal/service/data/codec/json — range 0.3.2.* (ADR 0005
// service/data/codec/json block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
package json

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.2.0 - 0.3.2.255

// CodeJSONMarshalFailed identifies a failure inside encoding/json.Marshal.
const CodeJSONMarshalFailed errs.Code = 0x00_03_02_01 // 0.3.2.1

// CodeJSONUnmarshalFailed identifies a failure inside encoding/json.Unmarshal.
const CodeJSONUnmarshalFailed errs.Code = 0x00_03_02_02 // 0.3.2.2
