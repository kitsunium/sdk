// Package asn1 declares the error codes and the sentinels of the ASN.1 DER
// codec, internal/service/data/codec/asn1 — range 0.3.9.* (ADR 0005
// service/data/codec/asn1 block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
package asn1

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.9.0 - 0.3.9.255

// CodeASN1MarshalFailed identifies a failure inside encoding/asn1.Marshal.
const CodeASN1MarshalFailed errs.Code = 0x00_03_09_01 // 0.3.9.1

// CodeASN1UnmarshalFailed identifies a failure inside encoding/asn1.Unmarshal.
const CodeASN1UnmarshalFailed errs.Code = 0x00_03_09_02 // 0.3.9.2
