// Package mail — one file or inline part, as a value.
package mail

// DefaultAttachmentType is the media type used when an attachment names none.
// RFC 2046 §4.5.1 makes application/octet-stream the type for "arbitrary
// binary data", and guessing a narrower one from the bytes would be a sniffer
// — the class of code that decides an uploaded .txt is really HTML.
const DefaultAttachmentType string = "application/octet-stream"

// AttachmentValue is one MIME body part carrying a file.
//
// The distinction between an attachment and an inline image is ONE field.
// A non-empty ContentID makes the part inline: it is placed in a
// multipart/related alongside the body (RFC 2387) and referenced from the HTML
// as cid:<ContentID> (RFC 2392 §2). An empty one makes it a regular attachment
// in a multipart/mixed (RFC 2046 §5.1.3).
//
// That is why there is no Disposition field to set wrongly. The disposition and
// the enclosing multipart subtype are two halves of one decision, and letting a
// caller spell them separately is letting them disagree — which is exactly the
// message that renders as a broken image in one client and a stray attachment
// in another.
type AttachmentValue struct {
	// Filename is the name presented to the recipient. It is required: an
	// attachment with no name is a part a client shows as "noname", and the
	// SDK will not invent one.
	Filename string
	// ContentType is the media type. Empty selects the type implied by the
	// filename's extension, and [DefaultAttachmentType] when there is none.
	ContentType string
	// ContentID, when non-empty, makes this part INLINE and referenceable as
	// cid:<ContentID>. It is an addr-spec-shaped token (RFC 2045 §7), written
	// without angle brackets — the composer adds them.
	ContentID string
	// Content is the part's bytes, base64 encoded at composition (RFC 2045
	// §6.8). An empty slice is a legal zero-length attachment.
	Content []byte
}

// Inline reports whether this part is referenced from the body rather than
// listed as a file.
func (a *AttachmentValue) Inline() bool {
	//: one field decides both the disposition and the enclosing subtype.
	return a.ContentID != ""
}
