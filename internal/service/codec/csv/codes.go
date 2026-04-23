// Package csv: codes.go — range 0.3.8.* (ADR 0005 service/codec/csv block).
package csv

// range: 0.3.8.0 - 0.3.8.255

// CodeCSVMarshalFailed identifies a failure inside encoding/csv.Writer.
const CodeCSVMarshalFailed = 0x00_03_08_01 // 0.3.8.1

// CodeCSVUnmarshalFailed identifies a failure inside encoding/csv.Reader.
const CodeCSVUnmarshalFailed = 0x00_03_08_02 // 0.3.8.2

// CodeCSVValueInvalid identifies a Marshal / Unmarshal call whose target is
// not a *[][]string (CSV only serialises a records matrix).
const CodeCSVValueInvalid = 0x00_03_08_03 // 0.3.8.3
