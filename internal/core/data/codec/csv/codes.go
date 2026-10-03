// Package csv declares the error codes and the sentinels of the CSV codec,
// internal/service/data/codec/csv — range 0.3.8.* (ADR 0005
// service/data/codec/csv block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
package csv

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.8.0 - 0.3.8.255

// CodeCSVMarshalFailed identifies a failure inside encoding/csv.Writer.
const CodeCSVMarshalFailed errs.Code = 0x00_03_08_01 // 0.3.8.1

// CodeCSVUnmarshalFailed identifies a failure inside encoding/csv.Reader.
const CodeCSVUnmarshalFailed errs.Code = 0x00_03_08_02 // 0.3.8.2

// CodeCSVValueInvalid identifies a Marshal / Unmarshal call whose target is
// not a *[][]string (CSV only serialises a records matrix).
const CodeCSVValueInvalid errs.Code = 0x00_03_08_03 // 0.3.8.3
