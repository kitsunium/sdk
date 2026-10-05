package mail

// DefaultAttachmentType is the media type used when an attachment names none.
// RFC 2046 §4.5.1 makes application/octet-stream the type for "arbitrary
// binary data", and guessing a narrower one from the bytes would be a sniffer
// — the class of code that decides an uploaded .txt is really HTML.
const DefaultAttachmentType string = "application/octet-stream"

// Inline reports whether this part is referenced from the body rather than
// listed as a file.
func (a *AttachmentValue) Inline() bool {
	//: one field decides both the disposition and the enclosing subtype.
	return a.ContentID != ""
}
