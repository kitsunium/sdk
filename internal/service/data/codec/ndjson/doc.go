// Package ndjson implements newline-delimited JSON as a codec.Codec.
// Each record is a JSON value followed by a single '\n' — the format is
// intentionally line-oriented so that partial reads remain parseable.
// The codec accepts slice values: Marshal emits one record per element,
// Unmarshal splits on '\n' and decodes each non-empty line into a new
// slice element.
package ndjson
