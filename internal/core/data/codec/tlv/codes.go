// Package tlv declares the error codes and the sentinels of the TLV codec,
// internal/service/data/codec/tlv — range 0.3.22.* (ADR 0006
// service/data/codec/tlv block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
package tlv

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.22.0 - 0.3.22.255

// CodeTLVMarshalFailed identifies an encode-side failure (reflection
// dispatch, unsupported value, writer error during streaming encode).
const CodeTLVMarshalFailed errs.Code = 0x00_03_16_01 // 0.3.22.1

// CodeTLVUnmarshalFailed identifies a decode-side failure (malformed
// record, type mismatch, reader error during streaming decode).
const CodeTLVUnmarshalFailed errs.Code = 0x00_03_16_02 // 0.3.22.2

// CodeTLVUnsupportedType identifies a value whose reflect.Kind cannot be
// TLV-encoded (chan, func, complex*, unsafe.Pointer).
const CodeTLVUnsupportedType errs.Code = 0x00_03_16_03 // 0.3.22.3

// CodeTLVDepthExceeded identifies a nested value whose depth exceeds the
// hard cap of 32 levels (CWE-674 stack-exhaustion defence).
const CodeTLVDepthExceeded errs.Code = 0x00_03_16_04 // 0.3.22.4

// CodeTLVSizeExceeded identifies an Unmarshal input whose length exceeds
// the 10 MiB hard cap (CWE-400 memory-exhaustion defence).
const CodeTLVSizeExceeded errs.Code = 0x00_03_16_05 // 0.3.22.5

// CodeTLVTruncated identifies a buffer that ends mid-record (length
// declared but value bytes missing, or varint declared but bytes missing).
const CodeTLVTruncated errs.Code = 0x00_03_16_06 // 0.3.22.6
