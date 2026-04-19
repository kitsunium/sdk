// Package baseenc: codes.go — range 4250-4259 for byte-encoding errors.
package baseenc

// range: 4250-4259

// CodeInvalidEncoding fires when the caller supplies an Encoding value that
// does not map to any stdlib encoding.
const CodeInvalidEncoding int = 4251

// CodeDecodeFailed fires when the underlying stdlib decoder rejects the
// input bytes for the requested Encoding.
const CodeDecodeFailed int = 4252

// CodeEncodeFailed fires when the underlying stdlib encoder rejects the
// raw bytes (e.g. ascii85 writer failure during buffered encode).
const CodeEncodeFailed int = 4253
