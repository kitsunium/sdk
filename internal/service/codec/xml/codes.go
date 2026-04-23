// Package xml: codes.go — range 0.3.3.* (ADR 0005 service/codec/xml block).
package xml

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.3.0 - 0.3.3.255

// CodeXMLMarshalFailed identifies a failure inside encoding/xml.Marshal.
const CodeXMLMarshalFailed errs.Code = 0x00_03_03_01 // 0.3.3.1

// CodeXMLUnmarshalFailed identifies a failure inside encoding/xml.Unmarshal.
const CodeXMLUnmarshalFailed errs.Code = 0x00_03_03_02 // 0.3.3.2
