// Package csv: codes.go — range 3270-3279 for CSV codec errors.
package csv

// range: 3270-3279

// CodeMarshalFailed identifies a failure inside encoding/csv.Writer.
const CodeMarshalFailed int = 3271

// CodeUnmarshalFailed identifies a failure inside encoding/csv.Reader.
const CodeUnmarshalFailed int = 3272

// CodeValueInvalid identifies a Marshal / Unmarshal call whose target is
// not a *[][]string (CSV only serialises a records matrix).
const CodeValueInvalid int = 3273
