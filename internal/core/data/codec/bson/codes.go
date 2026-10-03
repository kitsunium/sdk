// Package bson declares the error codes and the sentinels of the BSON codec,
// internal/service/data/codec/bson — range 0.3.36.* (ADR 0021
// service/data/codec/bson block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
package bson

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.36.0 - 0.3.36.255

// CodeBSONMarshalFailed identifies a value Marshal or Append cannot encode: a
// top level that is not a document, a type BSON has no representation for, a
// string that is not UTF-8, a key holding a NUL, or an error a MarshalBSON
// method returned.
const CodeBSONMarshalFailed errs.Code = 0x00_03_24_01 // 0.3.36.1

// CodeBSONUnmarshalFailed identifies input Unmarshal refuses: bytes that are
// not one well-formed BSON document, or a value the target cannot hold.
const CodeBSONUnmarshalFailed errs.Code = 0x00_03_24_02 // 0.3.36.2

// CodeBSONSizeExceeded identifies an Unmarshal input whose length exceeds the
// 10 MiB hard cap (CWE-400 memory-exhaustion defence before any byte is read).
const CodeBSONSizeExceeded errs.Code = 0x00_03_24_03 // 0.3.36.3

// CodeBSONDepthExceeded identifies a document or value nested deeper than
// maxBSONNestedLevels, in either direction (CWE-674 stack-exhaustion defence;
// on the encode side it is also what a cyclic value runs into).
const CodeBSONDepthExceeded errs.Code = 0x00_03_24_04 // 0.3.36.4

// CodeBSONValueInvalid identifies a BSON value type that cannot be built from
// what it was given: a malformed ObjectID hex string, a decimal128 string that
// is not a number or not exactly representable, a JSON form of either that is
// neither of the shapes accepted.
const CodeBSONValueInvalid errs.Code = 0x00_03_24_05 // 0.3.36.5
