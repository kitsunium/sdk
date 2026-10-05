package mail

// DefaultAttachmentType is the media type used when an attachment names none.
// RFC 2046 §4.5.1 makes application/octet-stream the type for "arbitrary
// binary data", and guessing a narrower one from the bytes would be a sniffer
// — the class of code that decides an uploaded .txt is really HTML.
const DefaultAttachmentType string = "application/octet-stream"

// inline is AttachmentValue.Inline's body: decl_gen.go writes AttachmentValue.Inline, from the
// design, as one call of it.
func (a *AttachmentValue) inline() bool {
	//: one field decides both the disposition and the enclosing subtype.
	return a.ContentID != ""
}
