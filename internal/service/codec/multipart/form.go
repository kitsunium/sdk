// Package multipart — the two value types that model a body: one section
// (PartValue) and the container that frames them (FormValue). They live in one
// file because neither is meaningful without the other.
package multipart

// FormValue is a whole multipart/form-data body: its RFC 2046 delimiter and
// its sections, in wire order.
//
// Boundary is the one field that has no equivalent in the codec.Codec
// contract, which carries no Content-Type. Empty on Marshal means "generate
// one"; Unmarshal always fills it with the delimiter it recovered, so a decode
// → encode round trip keeps the original delimiter. It reproduces the whole
// body byte for byte only for a body this package encoded: a PartValue models
// a part's name, filename, media type and bytes, so any other part header — a
// Content-Transfer-Encoding, a custom one — and another producer's header
// order or casing are not carried through.
type FormValue struct {
	// Boundary is the RFC 2046 delimiter framing the parts.
	Boundary string
	// Parts are the body's sections, in wire order.
	Parts []PartValue
}

// PartValue is one section of a multipart/form-data body: a named field,
// optionally a filename and a media type, and the bytes themselves.
//
// The body is materialised. Streaming happens BETWEEN parts (one part per
// Encode / Decode), not inside one: codec.Decoder's Decode(v any) has nowhere
// to hand back an io.Reader the caller must drain before the next call, so a
// part body is read whole under LimitsConfig.MaxPartBytes. A consumer moving
// objects too large for that ceiling raises it with NewWithLimits, or reaches
// for mime/multipart directly — see CLAUDE.md §Not covered.
//
// Name, FileName and ContentType are written into the part's header block, so
// a CR, an LF or a NUL in any of them is refused with ValueInvalid naming the
// field: a line break there would end the header line and let the rest of the
// value write headers of its own. Nothing is escaped or repaired on the way.
type PartValue struct {
	// Name is the form field name (Content-Disposition name=). Required: Encode
	// refuses a part without one, and decoding refuses a body carrying one.
	Name string
	// FileName is the optional filename= parameter; empty for a plain field.
	// UTF-8 is written as-is (RFC 7578 §4.2); only CR, LF and NUL are refused.
	FileName string
	// ContentType is the optional per-part media type; empty omits the header.
	ContentType string
	// Data is the part body.
	Data []byte
}
