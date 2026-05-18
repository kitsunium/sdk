// Package baseenc — range 1.2.1.* (ADR 0005 pkg/v1/codec/baseenc block).
package baseenc

import "github.com/kitsunium/sdk/pkg/v1/errs"

// range: 1.2.1.0 - 1.2.1.255

// CodeInvalidEncoding fires when the caller supplies an Encoding value that
// does not map to any stdlib encoding.
const CodeInvalidEncoding errs.Code = 0x01_02_01_01 // 1.2.1.1

// CodeDecodeFailed fires when the underlying stdlib decoder rejects the
// input bytes for the requested Encoding.
const CodeDecodeFailed errs.Code = 0x01_02_01_02 // 1.2.1.2

// CodeEncodeFailed fires when the underlying stdlib encoder rejects the
// raw bytes (e.g. ascii85 writer failure during buffered encode).
const CodeEncodeFailed errs.Code = 0x01_02_01_03 // 1.2.1.3
