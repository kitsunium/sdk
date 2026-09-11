// Package mail — the MIME entity tree and the writer that renders it.
//
// The renderer emits the multipart delimiters itself rather than using
// mime/multipart.Writer, and the reason is one measured line of that package:
// Writer.CreatePart formats header values VERBATIM, so a Content-Disposition
// carrying "a\r\nBcc: x@y" is emitted with the injected header intact. Every
// header written here goes through writeHeader, which runs the injection gate
// at the point of writing.
//
// The parser is the standard library's and always will be — mime/multipart's
// Reader is what the tests reparse composed messages with, because a test that
// decodes with the same code that encoded preserves exactly the bug it exists
// to catch.
package mail

import (
	"encoding/base64"
	"io"
	"mime/quotedprintable"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
)

// base64LineOctets is the RFC 2045 §6.8 ceiling: "no more than 76 characters"
// per encoded line.
const base64LineOctets int = 76

// Content-Transfer-Encoding values this domain emits (RFC 2045 §6).
const (
	encodingQuotedPrintable string = "quoted-printable"
	encodingBase64          string = "base64"
)

// entity is one MIME body part: its already-rendered header values, and either
// content or children.
//
// The header values are strings rather than structured fields because they are
// built once, by the composer, from values core/mail has already validated —
// and because the exact bytes of a Content-Type are what a test asserts on.
type entity struct {
	// contentType is the full Content-Type field value, parameters included.
	contentType string
	// encoding is the Content-Transfer-Encoding value, empty for a multipart
	// (RFC 2045 §6.4 restricts those to 7bit/8bit/binary, and 7bit is the
	// default, so emitting it says nothing).
	encoding string
	// disposition is the Content-Disposition value (RFC 2183), empty for a
	// body part that is not a file.
	disposition string
	// contentID is the Content-ID value INCLUDING its angle brackets, empty
	// unless this part is referenced by a cid: URL.
	contentID string
	// boundary is non-empty exactly when this entity is a multipart.
	boundary string
	// content is a leaf's decoded bytes.
	content []byte
	// text marks a leaf whose content is text and is therefore
	// quoted-printable rather than base64.
	text bool
	// children are a multipart's parts, in the order they must appear.
	children []entity
}

// writeHeaders emits this entity's own MIME header fields, in a fixed order so
// a composed message is byte-deterministic.
func (e entity) writeHeaders(dst io.Writer) error {
	//: Content-Type first: it is the field a receiver dispatches on.
	if typeErr := writeHeader(dst, coremail.HeaderContentType, e.contentType); typeErr != nil {
		//: HeaderInjection or HeaderTooLong.
		return typeErr
	}
	//: then the encoding, absent on a multipart.
	if e.encoding != "" {
		//: RFC 2045 §6.
		if encErr := writeHeader(dst, coremail.HeaderContentTransferEncoding, e.encoding); encErr != nil {
			//: HeaderInjection or HeaderTooLong.
			return encErr
		}
	}
	//: then the disposition, absent on a body part.
	if e.disposition != "" {
		//: RFC 2183.
		if dispErr := writeHeader(dst, coremail.HeaderContentDisposition, e.disposition); dispErr != nil {
			//: HeaderInjection or HeaderTooLong.
			return dispErr
		}
	}
	//: and finally the identifier a cid: URL resolves against.
	if e.contentID != "" {
		//: RFC 2045 §7 / RFC 2392 §2.
		return writeHeader(dst, coremail.HeaderContentID, e.contentID)
	}
	//: no identifier on this part.
	return nil
}

// writeBody emits this entity's content: the encoded bytes of a leaf, or the
// delimited parts of a multipart.
func (e entity) writeBody(dst io.Writer) error {
	//: a multipart's body is its parts, framed by its boundary.
	if e.boundary != "" {
		//: RFC 2046 §5.1.1.
		return e.writeMultipartBody(dst)
	}
	//: a text leaf goes out quoted-printable: it guarantees the RFC 5322
	//: §2.1.1 line limits and 7-bit safety without needing 8BITMIME, which no
	//: transport here has negotiated.
	if e.text {
		//: RFC 2045 §6.7. The writer also normalises lone LF and lone CR to
		//: CRLF, which is the line ending §2.1 requires.
		writer := quotedprintable.NewWriter(dst)
		//: one write of the whole body.
		if _, writeErr := writer.Write(e.content); writeErr != nil {
			//: the underlying buffer failed.
			return writeErr
		}
		//: flush the trailing soft break, if any.
		return writer.Close()
	}
	//: everything else is base64 at 76 octets per line (RFC 2045 §6.8).
	return writeBase64(dst, e.content)
}

// writeMultipartBody frames every child with this entity's boundary, per
// RFC 2046 §5.1.1.
//
// No preamble and no epilogue are written. Both are legal and both are ignored
// by every conforming receiver, and the "this is a multi-part message in MIME
// format" preamble every mail client emits is addressed to readers who have
// not existed for two decades.
func (e entity) writeMultipartBody(dst io.Writer) error {
	//: each part opens with the dash-boundary on its own line.
	for _, child := range e.children {
		//: the delimiter, then the part's headers, then a blank line.
		if _, err := io.WriteString(dst, "--"+e.boundary+crlf); err != nil {
			//: the buffer failed.
			return err
		}
		//: the child's own MIME headers.
		if headerErr := child.writeHeaders(dst); headerErr != nil {
			//: HeaderInjection or HeaderTooLong.
			return headerErr
		}
		//: the empty line that ends a header block (RFC 5322 §2.1).
		if _, err := io.WriteString(dst, crlf); err != nil {
			//: the buffer failed.
			return err
		}
		//: and the child's body, recursively.
		if bodyErr := child.writeBody(dst); bodyErr != nil {
			//: propagated unchanged.
			return bodyErr
		}
		//: the CRLF that belongs to the NEXT delimiter, not to this body
		//: (RFC 2046 §5.1.1: "delimiter := CRLF dash-boundary").
		if _, err := io.WriteString(dst, crlf); err != nil {
			//: the buffer failed.
			return err
		}
	}
	//: the close-delimiter ends the multipart.
	_, err := io.WriteString(dst, "--"+e.boundary+"--"+crlf)
	//: the buffer's own failure, if any.
	return err
}

// writeBase64 encodes content at [base64LineOctets] characters per line,
// streaming rather than materialising the whole encoded string.
//
// The wrapping matters beyond conformance: an unwrapped 1 MiB attachment is one
// 1.4-million-octet line, which violates RFC 5322 §2.1.1 by three orders of
// magnitude and is truncated — silently — by more than one MTA.
func writeBase64(dst io.Writer, content []byte) error {
	//: an empty attachment is a legal zero-length part and encodes to nothing.
	if len(content) == 0 {
		//: no line, not even an empty one.
		return nil
	}
	wrapper := &lineWrapper{dst: dst, width: base64LineOctets}
	encoder := base64.NewEncoder(base64.StdEncoding, wrapper)
	//: one pass over the input, no intermediate string.
	if _, writeErr := encoder.Write(content); writeErr != nil {
		//: the buffer failed.
		return writeErr
	}
	//: flush the encoder's partial group and its padding.
	if closeErr := encoder.Close(); closeErr != nil {
		//: the encoder failed.
		return closeErr
	}
	//: terminate the final, short line.
	return wrapper.finish()
}
