// Package json — range 0.3.2.* (ADR 0005 service/codec/json block).
package json

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.2.0 - 0.3.2.255

// CodeJSONMarshalFailed identifies a failure inside encoding/json.Marshal.
const CodeJSONMarshalFailed errs.Code = 0x00_03_02_01 // 0.3.2.1

// CodeJSONUnmarshalFailed identifies a failure inside encoding/json.Unmarshal.
const CodeJSONUnmarshalFailed errs.Code = 0x00_03_02_02 // 0.3.2.2
