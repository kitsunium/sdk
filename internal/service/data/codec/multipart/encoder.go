package multipart

import (
	"crypto/rand"
	"encoding/hex"
	stdjson "encoding/json"
	"io"
	"strings"

	coremultipart "github.com/kitsunium/sdk/internal/core/data/codec/multipart"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// forbiddenHeaderOctets are the three octets no part header value may carry.
// A header value is written verbatim, so a CR or an LF inside one does not
// corrupt the field — it ENDS the line, and the rest of the value is read back
// as a header line of its own or, after an empty line, as the start of the
// body. NUL ends the value for any consumer written in C. The same three
// octets ADR 0064's mail header gate refuses.
const forbiddenHeaderOctets string = "\r\n\x00"

// The framing the encoder writes, byte for byte what mime/multipart.Writer
// writes: its CreatePart and Close are the oracle the tests hold it to (#907).
const (
	// crlf ends every line of the framing.
	crlf string = "\r\n"
	// dispositionOpen opens a part's Content-Disposition header.
	dispositionOpen string = `Content-Disposition: form-data; name="`
	// filenameOpen opens the filename parameter of a file part.
	filenameOpen string = `; filename="`
	// contentTypeOpen opens a part's optional Content-Type header.
	contentTypeOpen string = "Content-Type: "
	// headScratchBytes sizes the buffer a part's delimiter and header block
	// are built in, once per encoder: a form field's name and a short
	// filename fit, a longer header grows it once.
	headScratchBytes int = 160
	// generatedBoundaryBytes is how many random bytes a generated boundary
	// spells in hex — mime/multipart's 30, so 60 characters.
	generatedBoundaryBytes int = 30
)

// multipartEncoder writes a multipart/form-data body. One Encode call writes
// exactly one part; Close writes the terminating delimiter. It frames the
// parts itself rather than through mime/multipart.Writer, whose CreatePart
// built a textproto.MIMEHeader per part and formatted and sorted it through
// fmt — about seventeen allocations a part, for bytes that are a handful of
// appends (#907).
type multipartEncoder struct {
	// w receives the body; the encoder never closes it.
	w io.Writer
	// boundary is the delimiter, generated or pinned.
	boundary string
	// head is the reused buffer each part's delimiter and header block are
	// built in before they are written.
	head []byte
	// count charges the bounds per part — the same LimitsConfig as the
	// decoder's, so the encoder never emits a body this package would refuse
	// to read back.
	count counter
	// open is set once a part has been written since the encoder began or
	// last closed: the next delimiter then starts on a new line.
	open bool
}

// Encode writes v as one part of the multipart body.
//
// A PartValue (or *PartValue) is written verbatim. Any other value takes the
// JSON-mediated shape: a part named JSONPartName carrying encoding/json's
// output — the same JSON bridge pkg/v1/data/codec's promotion path would build,
// applied inside the codec so Marshal(F, anyValue) holds natively.
func (e *multipartEncoder) Encode(v any) error {
	//: resolve the value to a concrete part (native or JSON-mediated).
	part, err := asPart(v)
	//: a part: its header block and body, once the bounds admit them. A shape
	//: or JSON failure writes nothing and is returned verbatim.
	if err == nil {
		err = e.writePart(part)
	}
	return err
}

// Close writes the closing delimiter. The wrapped io.Writer is NOT closed —
// the encoder never owns it, matching every other streaming codec here.
func (e *multipartEncoder) Close() error {
	closing := e.closingLen()
	//: the closing delimiter counts against MaxTotalBytes like any byte the
	//: decoder will read; a part admitted kept room for it.
	if lerr := e.count.admitClosing(closing); lerr != nil {
		//: typed LIMIT_EXCEEDED.
		return lerr
	}
	e.head = append(append(append(append(e.head[:0], crlf...), delimiterPrefix...), e.boundary...), delimiterPrefix+crlf...)
	e.open = false
	//: without it the body is truncated and every reader reports an
	//: unexpected EOF.
	if _, werr := e.w.Write(e.head); werr != nil {
		//: wrap the transport error for reason-based matching.
		return marshalFailed(werr, "Encoder.Close: writing the closing delimiter failed")
	}
	//: the body is complete.
	return nil
}

// Boundary returns the delimiter this encoder writes, so a caller streaming a
// request body can set its Content-Type header BEFORE the first byte goes out.
// Reachable through the BoundaryProvider assertion on the codec.Encoder.
func (e *multipartEncoder) Boundary() string {
	//: generated at construction or pinned by the caller.
	return e.boundary
}

// writePart emits one part's header block followed by its body.
func (e *multipartEncoder) writePart(part *PartValue) error {
	//: a form-data part without a field name has no addressable identity.
	if part.Name == "" {
		//: typed refusal — a programming error, not a wire fault.
		return valueInvalid("part carries no field name")
	}
	//: a CR, LF or NUL in a header value would end its line and write the rest
	//: as header lines the caller never wrote — refused before a byte is
	//: written, and never repaired (ADR 0064's stance).
	if field, found := injectedHeaderField(part); found {
		//: typed refusal naming the field; the value is never echoed.
		return valueInvalid("a part header value carries a CR, LF or NUL",
			errs.String("field", field))
	}
	e.head = e.appendHead(e.head[:0], part)
	wire := int64(len(e.head)) + int64(len(part.Data))
	//: charge the part against the three bounds before writing a byte of it.
	if lerr := e.count.admitEncodedPart(int64(len(part.Data)), wire, e.closingLen()); lerr != nil {
		//: typed LIMIT_EXCEEDED; the body is left mid-stream for the caller
		//: to discard, exactly as a failed io.Writer would.
		return lerr
	}
	e.open = true
	//: the delimiter and the header block in one write.
	if _, werr := e.w.Write(e.head); werr != nil {
		//: wrap with the marshal sentinel.
		return marshalFailed(werr, "Encoder.Encode: writing the part header failed")
	}
	//: the body; a short write is reported as an error by the io.Writer contract.
	if _, werr := e.w.Write(part.Data); werr != nil {
		//: wrap with the marshal sentinel.
		return marshalFailed(werr, "Encoder.Encode: writing the part body failed")
	}
	//: part complete.
	return nil
}

// appendHead appends a part's delimiter line and its RFC 7578 header block to
// b: the field name always, the filename and the Content-Type only when set,
// both quoted parameters escaping the backslash and the double quote — the
// bytes mime/multipart.Writer.CreatePart writes for the same header, whose
// keys it sorts, Content-Disposition before Content-Type.
func (e *multipartEncoder) appendHead(b []byte, part *PartValue) []byte {
	//: a delimiter after a part starts on a new line.
	if e.open {
		b = append(b, crlf...)
	}
	b = append(append(append(b, delimiterPrefix...), e.boundary...), crlf...)
	b = appendQuoted(append(b, dispositionOpen...), part.Name)
	//: a file part also carries its filename.
	if part.FileName != "" {
		b = appendQuoted(append(append(b, '"'), filenameOpen...), part.FileName)
	}
	b = append(append(b, '"'), crlf...)
	//: Content-Type is optional; an empty value is simply omitted.
	if part.ContentType != "" {
		b = append(append(append(b, contentTypeOpen...), part.ContentType...), crlf...)
	}
	//: the empty line that ends the header block.
	return append(b, crlf...)
}

// closingLen is the length of the closing delimiter this encoder writes.
func (e *multipartEncoder) closingLen() int64 {
	//: CRLF, "--", the boundary, "--", CRLF.
	return int64(len(crlf) + 2*len(delimiterPrefix) + len(e.boundary) + len(crlf))
}

// appendQuoted appends s escaping the two characters that can end or bend a
// quoted header parameter: the backslash and the double quote.
//
// It is deliberately NOT mime/multipart's own (unexported) escaper, which also
// percent-encodes CR and LF. That encoding is a REPAIR — the part would carry a
// filename the caller never supplied while Encode reported success. This
// package refuses those octets instead, before any header is built (see
// forbiddenHeaderOctets), so a value reaching here holds no CR, LF or NUL.
func appendQuoted(b []byte, s string) []byte {
	//: one byte at a time; the two specials get a backslash first.
	for i := range len(s) {
		//: escape the specials.
		if s[i] == '\\' || s[i] == '"' {
			b = append(b, '\\')
		}
		b = append(b, s[i])
	}
	//: escaped.
	return b
}

// generateBoundary draws a delimiter as mime/multipart.Writer does: thirty
// random bytes from crypto/rand, in lower-case hex.
func generateBoundary() string {
	var raw [generatedBoundaryBytes]byte
	//: crypto/rand.Read never fails on a supported platform (Go 1.24).
	_, _ = rand.Read(raw[:])
	//: sixty characters, every one in the RFC 2046 set.
	return hex.EncodeToString(raw[:])
}

// injectedHeaderField reports the first of the three caller-supplied header
// values carrying a forbidden octet, in declaration order, so a part with two
// bad values still names one of them deterministically. It reports the FIELD
// and never the value: the value is exactly the string an attacker chose.
func injectedHeaderField(part *PartValue) (field string, found bool) {
	//: the field name lands in Content-Disposition's name= parameter.
	if strings.ContainsAny(part.Name, forbiddenHeaderOctets) {
		//: name the field, not its value.
		return "Name", true
	}
	//: the filename lands in Content-Disposition's filename= parameter.
	if strings.ContainsAny(part.FileName, forbiddenHeaderOctets) {
		//: name the field, not its value.
		return "FileName", true
	}
	//: the media type is written as the whole Content-Type value.
	if strings.ContainsAny(part.ContentType, forbiddenHeaderOctets) {
		//: name the field, not its value.
		return "ContentType", true
	}
	//: every header value fits on its own line.
	return "", false
}

// asPart resolves an arbitrary value to the part the encoder will write.
func asPart(v any) (part *PartValue, err error) {
	//: native shapes are written verbatim.
	switch typed := v.(type) {
	//: value form — take its address so the 72-byte struct travels by pointer.
	case PartValue:
		//: nothing to resolve.
		return &typed, nil
	//: pointer form — a nil pointer carries no part.
	case *PartValue:
		//: reject before dereferencing.
		if typed == nil {
			//: typed refusal.
			return nil, valueInvalid("Encode called with a nil *PartValue")
		}
		//: already the shape we want.
		return typed, nil
	}
	//: JSON-mediated shape for every other value.
	inner, jerr := stdjson.Marshal(v)
	//: a value encoding/json cannot serialise is a marshal failure.
	if jerr != nil {
		//: wrap the stdlib error for reason-based matching.
		return nil, marshalFailed(jerr, "encoding/json rejected the value")
	}
	//: single JSON part, named so Unmarshal can find it again.
	return &PartValue{Name: JSONPartName, ContentType: mimeJSON, Data: inner}, nil
}

// marshalFailed wraps cause with the MarshalFailed sentinel and a call-site
// specific Private diagnostic.
func marshalFailed(cause error, detail string) error {
	//: typed sentinel so callers route on CodeMultipartMarshalFailed.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    coremultipart.CodeMultipartMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "multipart encoding failed",
		Private: "service/data/codec/multipart." + detail,
	})
}

// valueInvalid builds the typed ValueInvalid error for a rejected argument
// shape, with a call-site specific Private diagnostic and any fields that
// locate the problem (never a caller-supplied value).
func valueInvalid(detail string, fields ...errs.FieldValue) error {
	//: typed sentinel so callers route on CodeMultipartValueInvalid.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    coremultipart.CodeMultipartValueInvalid,
		Reason:  "VALUE_INVALID",
		Public:  "multipart codec rejected the value shape",
		Private: "service/data/codec/multipart: " + detail,
	}, fields...)
}
