// Package hcl — range 0.3.37.* (ADR 0022 third-party/codec/hcl block).
package hcl

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.37.0 - 0.3.37.255

// CodeHCLMarshalFailed identifies a failure encoding a value to HCL via
// gohcl.EncodeIntoBody (e.g. a top-level non-struct value, or an unsupported
// field type — recovered from the library panic).
const CodeHCLMarshalFailed errs.Code = 0x00_03_25_01 // 0.3.37.1

// CodeHCLUnmarshalFailed identifies a failure parsing/decoding HCL via
// hclsimple.Decode (syntax error or schema mismatch).
const CodeHCLUnmarshalFailed errs.Code = 0x00_03_25_02 // 0.3.37.2

// CodeHCLSizeExceeded identifies an Unmarshal input over the 10 MiB cap
// (CWE-400 defence before the parser allocates).
const CodeHCLSizeExceeded errs.Code = 0x00_03_25_03 // 0.3.37.3
