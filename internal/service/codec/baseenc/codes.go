// Package baseenc — range 0.3.24.* (ADR 0006 service/codec/baseenc block).
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
