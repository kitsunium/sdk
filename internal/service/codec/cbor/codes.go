// Package cbor — range 0.3.6.* (ADR 0005 service/codec/cbor block).
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
