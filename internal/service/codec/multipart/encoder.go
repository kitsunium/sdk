// Package multipart — adapts mime/multipart.Writer to codec.Encoder.
//
// This is the ONLY write path in the package: Marshal and Append both drive
// this encoder over a scratch buffer, so a body built in memory and a body
// streamed to a socket go through identical code and identical bounds.
package multipart

import (
	stdjson "encoding/json"
	stdmp "mime/multipart"
	"net/textproto"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// partHeaderFields is the number of headers a part carries at most —
// Content-Disposition plus an optional Content-Type. Used to size the map in
// one shot rather than letting it grow.
const partHeaderFields int = 2

// forbiddenHeaderOctets are the three octets no part header value may carry.
// mime/multipart.Writer.CreatePart writes every header value verbatim, so a CR
// or an LF inside one does not corrupt the field — it ENDS the line, and the
// rest of the value is read back as a header line of its own or, after an
// empty line, as the start of the body. NUL ends the value for any consumer
// written in C. The same three octets ADR 0064's mail header gate refuses.
const forbiddenHeaderOctets string = "\r\n\x00"

// quoteEscaper escapes the two characters that can end or bend a quoted header
// parameter: the backslash and the double quote.
//
// It is deliberately NOT a copy of mime/multipart's own (unexported) escaper,
// which also percent-encodes CR and LF. That encoding is a REPAIR — the part
// would carry a filename the caller never supplied while Encode reported
// success. This package refuses those octets instead, before any header is
// built (see forbiddenHeaderOctets), so a value reaching this replacer holds no
// CR, LF or NUL left to encode.
//
//nolint:gochecknoglobals // strings.Replacer is immutable and safe to share.
var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

// multipartEncoder adapts *mime/multipart.Writer to codec.Encoder. One Encode
// call writes exactly one part; Close writes the terminating delimiter.
type multipartEncoder struct {
	// inner is the stdlib writer that owns the delimiter and the framing.
	inner *stdmp.Writer
	// count charges the bounds per part — identical to the decoder's set, so
	// the encoder never emits a body this package would refuse to read back.
	count counter
}

// Encode writes v as one part of the multipart body.
//
// A PartValue (or *PartValue) is written verbatim. Any other value takes the
// JSON-mediated shape: a part named JSONPartName carrying encoding/json's
// output — the same JSON bridge pkg/v1/codec's promotion path would build,
// applied inside the codec so Marshal(F, anyValue) holds natively.
func (e *multipartEncoder) Encode(v any) error {
	//: resolve the value to a concrete part (native or JSON-mediated).
	part, perr := asPart(v)
	//: shape or JSON failure — nothing has been written yet.
	if perr != nil {
		//: propagate the typed refusal verbatim.
		return perr
	}
	//: charge the part against the three bounds before materialising it.
	if lerr := e.count.admitPart(int64(len(part.Data))); lerr != nil {
		//: typed LIMIT_EXCEEDED; the body is left mid-stream for the caller
		//: to discard, exactly as a failed io.Writer would.
		return lerr
	}
	//: write the header block and the body.
	return e.writePart(part)
}

// Close writes the closing delimiter. The wrapped io.Writer is NOT closed —
// the encoder never owns it, matching every other streaming codec here.
func (e *multipartEncoder) Close() error {
	//: multipart.Writer.Close writes "--<boundary>--"; without it the body
	//: is truncated and every reader reports an unexpected EOF.
	cerr := e.inner.Close()
	//: success fast-path.
	if cerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the stdlib error for reason-based matching.
	return errs.Wrap(cerr, errs.WrapParams{
		Code:    CodeMultipartMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "multipart encoding failed",
		Private: "service/codec/multipart.Encoder.Close: mime/multipart returned an error",
	})
}

// Boundary returns the delimiter this encoder writes, so a caller streaming a
// request body can set its Content-Type header BEFORE the first byte goes out.
// Reachable through the BoundaryProvider assertion on the codec.Encoder.
func (e *multipartEncoder) Boundary() string {
	//: delegate to the stdlib writer, which owns the generated value.
	return e.inner.Boundary()
}

// writePart emits one part's header block followed by its body.
func (e *multipartEncoder) writePart(part *PartValue) error {
	//: a form-data part without a field name has no addressable identity.
	if part.Name == "" {
		//: typed refusal — a programming error, not a wire fault.
		return valueInvalid("part carries no field name")
	}
	//: a CR, LF or NUL in a header value would end its line and write the rest
	//: as header lines the caller never wrote — refused before CreatePart puts
	//: a byte on the wire, and never repaired (ADR 0064's stance).
	if field, found := injectedHeaderField(part); found {
		//: typed refusal naming the field; the value is never echoed.
		return valueInvalid("a part header value carries a CR, LF or NUL",
			errs.String("field", field))
	}
	//: open the part; CreatePart writes the delimiter + header block.
	body, cerr := e.inner.CreatePart(partHeader(part))
	//: header write failure.
	if cerr != nil {
		//: wrap with the marshal sentinel.
		return marshalFailed(cerr, "Encoder.Encode: CreatePart returned an error")
	}
	//: body write; a short write is reported as an error by the stdlib.
	if _, werr := body.Write(part.Data); werr != nil {
		//: wrap with the marshal sentinel.
		return marshalFailed(werr, "Encoder.Encode: writing the part body failed")
	}
	//: part complete.
	return nil
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

// partHeader builds the RFC 7578 header block for part.
func partHeader(part *PartValue) textproto.MIMEHeader {
	//: Content-Disposition always carries the field name.
	disposition := `form-data; name="` + quoteEscaper.Replace(part.Name) + `"`
	//: a file part additionally carries its filename.
	if part.FileName != "" {
		//: append the filename parameter, escaped the same way.
		disposition += `; filename="` + quoteEscaper.Replace(part.FileName) + `"`
	}
	//: sized once for the two headers a part can carry.
	header := make(textproto.MIMEHeader, partHeaderFields)
	//: canonical key casing is applied by Set.
	header.Set("Content-Disposition", disposition)
	//: Content-Type is optional; an empty value is simply omitted.
	if part.ContentType != "" {
		//: preserve the caller's media type verbatim.
		header.Set("Content-Type", part.ContentType)
	}
	//: ready for CreatePart.
	return header
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
		Code:    CodeMultipartMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "multipart encoding failed",
		Private: "service/codec/multipart." + detail,
	})
}

// valueInvalid builds the typed ValueInvalid error for a rejected argument
// shape, with a call-site specific Private diagnostic and any fields that
// locate the problem (never a caller-supplied value).
func valueInvalid(detail string, fields ...errs.FieldValue) error {
	//: typed sentinel so callers route on CodeMultipartValueInvalid.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeMultipartValueInvalid,
		Reason:  "VALUE_INVALID",
		Public:  "multipart codec rejected the value shape",
		Private: "service/codec/multipart: " + detail,
	}, fields...)
}
