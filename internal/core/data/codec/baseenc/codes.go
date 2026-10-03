// Package baseenc declares the error codes and the sentinels of the base-N
// codec family (base64, base64url, base32, base16, hex, ascii85, base45,
// base58, base62), internal/service/data/codec/baseenc — range 0.3.24.* (ADR
// 0006 service/data/codec/baseenc block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
package baseenc

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.24.0 - 0.3.24.255

// CodeBaseEncMarshalFailed identifies a JSON-encode failure that happens
// before the base-N step in the universal "flatten to JSON, then base-N"
// pipeline.
const CodeBaseEncMarshalFailed errs.Code = 0x00_03_18_01 // 0.3.24.1

// CodeBaseEncUnmarshalFailed identifies a JSON-decode failure that happens
// after the base-N step in the universal "base-N decode, then JSON" pipeline.
const CodeBaseEncUnmarshalFailed errs.Code = 0x00_03_18_02 // 0.3.24.2

// CodeBaseEncDecodeFailed identifies a base-N decode failure (malformed
// input rejected by the stdlib encoding/{hex,base32,base64,ascii85}
// decoders).
const CodeBaseEncDecodeFailed errs.Code = 0x00_03_18_03 // 0.3.24.3

// CodeBaseEncSizeExceeded identifies an Unmarshal input whose length
// exceeds the 10 MiB hard cap (CWE-400 memory-exhaustion defence).
const CodeBaseEncSizeExceeded errs.Code = 0x00_03_18_04 // 0.3.24.4
