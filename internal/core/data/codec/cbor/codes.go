// Package cbor declares the error codes and the sentinels of the CBOR codec,
// internal/service/data/codec/cbor — range 0.3.6.* (ADR 0005
// service/data/codec/cbor block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
package cbor

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.6.0 - 0.3.6.255

// CodeCBORMarshalFailed identifies an encoding failure: a value of a type
// CBOR cannot carry, a string that is not UTF-8, a nesting deeper than the
// decoder accepts, a failing writer, MarshalBinary or MarshalCBOR.
const CodeCBORMarshalFailed errs.Code = 0x00_03_06_01 // 0.3.6.1

// CodeCBORUnmarshalFailed identifies a decoding failure: input that is not
// one well-formed, valid data item, a bound exceeded, or an item the target
// cannot hold.
const CodeCBORUnmarshalFailed errs.Code = 0x00_03_06_02 // 0.3.6.2
