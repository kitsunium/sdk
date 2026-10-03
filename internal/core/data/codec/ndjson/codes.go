// Package ndjson declares the error codes and the sentinels of the NDJSON
// codec, internal/service/data/codec/ndjson — range 0.3.11.* (ADR 0005
// service/data/codec/ndjson block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
package ndjson

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.11.0 - 0.3.11.255

// CodeNDJSONMarshalFailed identifies a failure inside encoding/json.Marshal
// when encoding a single NDJSON record.
const CodeNDJSONMarshalFailed errs.Code = 0x00_03_0B_01 // 0.3.11.1

// CodeNDJSONUnmarshalFailed identifies a failure inside encoding/json.Unmarshal
// when decoding a single NDJSON record.
const CodeNDJSONUnmarshalFailed errs.Code = 0x00_03_0B_02 // 0.3.11.2

// CodeNDJSONValueInvalid identifies a Marshal / Unmarshal call whose target is
// not a slice (NDJSON models a stream of records).
const CodeNDJSONValueInvalid errs.Code = 0x00_03_0B_03 // 0.3.11.3
