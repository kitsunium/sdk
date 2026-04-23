// Package json: codes.go — range 0.3.2.* (ADR 0005 service/codec/json block).
package json

// range: 0.3.2.0 - 0.3.2.255

// CodeJSONMarshalFailed identifies a failure inside encoding/json.Marshal.
const CodeJSONMarshalFailed = 0x00_03_02_01 // 0.3.2.1

// CodeJSONUnmarshalFailed identifies a failure inside encoding/json.Unmarshal.
const CodeJSONUnmarshalFailed = 0x00_03_02_02 // 0.3.2.2
