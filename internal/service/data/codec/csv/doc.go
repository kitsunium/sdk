// Package csv wraps encoding/csv as a codec.Codec implementation.
// The CSV format serialises a matrix of strings; the codec accepts
// [][]string for Marshal and writes into *[][]string on Unmarshal.
//
// A slice of structs is a table too (#902): Marshal writes a header from the
// columns its fields map — a csv:"name" tag, else the Go name; "-" skips a
// field, ",optional" lets a file lack its column — then one record per row,
// and Unmarshal into a pointer to such a slice reads the header and converts
// each cell to its field: strings, integers, floats, bools, and any type that
// reads and writes itself as text (time.Time in RFC 3339). Unknown columns
// are ignored. It is a codec.StreamingCodec: one record per Decode and
// Encode, a []string or a struct.
package csv
