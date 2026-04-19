// Package csv: codes.go — range 3270-3279 for CSV codec errors.
package csv

// range: 3270-3279

// CodeCSVMarshalFailed identifies a failure inside encoding/csv.Writer.
const CodeCSVMarshalFailed int = 3271

// CodeCSVUnmarshalFailed identifies a failure inside encoding/csv.Reader.
const CodeCSVUnmarshalFailed int = 3272

// CodeCSVValueInvalid identifies a Marshal / Unmarshal call whose target is
// not a *[][]string (CSV only serialises a records matrix).
const CodeCSVValueInvalid int = 3273
