// Package csv wraps encoding/csv as a codec.Codec implementation.
// The CSV format serialises a matrix of strings; the codec accepts
// [][]string for Marshal and writes into *[][]string on Unmarshal.
package csv
