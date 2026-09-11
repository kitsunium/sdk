// Package codec — the multipart/form-data value types, and the one helper a
// consumer needs to send what Marshal(Multipart, …) returns. The format is a
// container whose delimiter lives in the Content-Type header, which the Codec
// contract cannot carry, so the facade publishes the two shapes the codec
// speaks natively and the function that recovers that header from the body.
package codec

import svcmultipart "github.com/kitsunium/sdk/internal/service/codec/multipart"

// MultipartForm is the native Go shape of the [Multipart] format: a whole
// multipart/form-data body — its RFC 2046 boundary and its parts, in wire
// order. Marshal one to build an upload; Unmarshal into a *MultipartForm to
// read one. An empty Boundary asks Marshal to generate a delimiter; Unmarshal
// always fills it with the one it recovered, so a decode → encode round trip
// keeps the original delimiter — and reproduces the whole body byte for byte
// only for a body this codec encoded, since a part header other than the name,
// filename and media type is not carried through.
type MultipartForm = svcmultipart.FormValue

// MultipartPart is one section of a [MultipartForm]: a named field, optionally
// a filename and a media type, and the bytes themselves — a file upload is a
// part with FileName and ContentType set. Marshal also accepts a single
// MultipartPart or a []MultipartPart.
//
// Name is required. Name, FileName and ContentType are written into the part's
// header block, so a CR, an LF or a NUL in any of them is refused — never
// escaped — with the codec's VALUE_INVALID naming the field. A UTF-8 filename
// is written as-is (RFC 7578 §4.2).
type MultipartPart = svcmultipart.PartValue

// MultipartContentType returns the Content-Type header value —
// "multipart/form-data; boundary=…", quoted when the boundary needs it — for a
// body Marshal(Multipart, …) returned. Send it alongside the bytes: the
// boundary is announced in that header, and since the Codec contract has
// nowhere to hand one back, it is read off the body's first delimiter line. A
// body with no recoverable delimiter is refused with the codec's
// BOUNDARY_INVALID.
//
// A caller streaming through NewEncoder(Multipart, w) must set the header
// before the first byte instead: the Encoder returned has a Boundary() string
// method, and mime.FormatMediaType builds the same value from it.
func MultipartContentType(body []byte) (value string, err error) {
	//: delegate to the service helper, which owns the boundary recovery.
	return svcmultipart.ContentType(body)
}
