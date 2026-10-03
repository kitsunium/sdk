// Package xml declares the error codes and the sentinels of the XML codec,
// internal/service/data/codec/xml — range 0.3.3.* (ADR 0005
// service/data/codec/xml block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
package xml

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.3.0 - 0.3.3.255

// CodeXMLMarshalFailed identifies a failure inside encoding/xml.Marshal.
const CodeXMLMarshalFailed errs.Code = 0x00_03_03_01 // 0.3.3.1

// CodeXMLUnmarshalFailed identifies a failure inside encoding/xml.Unmarshal.
const CodeXMLUnmarshalFailed errs.Code = 0x00_03_03_02 // 0.3.3.2
