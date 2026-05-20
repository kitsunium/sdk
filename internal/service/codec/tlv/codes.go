// Package tlv — range 0.3.22.* (ADR 0006 service/codec/tlv block).
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
