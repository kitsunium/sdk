// Package mail — the attachment guards.
package mail

import (
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// pathSeparators are the two characters a filename may not carry. A path
// separator in a filename is how an unpacking client is talked into writing
// outside the directory it chose.
const pathSeparators string = `/\`

// ValidateAttachment reports whether a can be composed into a body part, and
// returns [InvalidAttachment] naming the position when it cannot.
//
// The filename faces the injection gate like any other header value, because
// it lands in Content-Disposition. mime.FormatMediaType would silently
// percent-escape a CRLF there — the same "repair the caller never hears about"
// this domain refuses everywhere else.
func ValidateAttachment(field string, a *AttachmentValue) error {
	//: an unnamed attachment shows as "noname"; naming it is the caller's job.
	if a.Filename == "" {
		//: the position is what the caller must fix.
		return errs.Wrap(InvalidAttachment, errs.WrapParams{},
			errs.String("field", field), errs.String("problem", "empty filename"))
	}
	//: the filename reaches Content-Disposition, so it faces the same gate.
	if nameErr := ValidateHeaderValue(field+".Filename", a.Filename); nameErr != nil {
		//: HeaderInjection.
		return nameErr
	}
	//: refused rather than stripped: stripping changes the name the recipient
	//: sees without telling anyone.
	if strings.ContainsAny(a.Filename, pathSeparators) {
		//: InvalidAttachment.
		return errs.Wrap(InvalidAttachment, errs.WrapParams{},
			errs.String("field", field), errs.String("problem", "path separator in filename"))
	}
	//: a declared media type must be spellable as one.
	if typeErr := validateMediaType(field, a.ContentType); typeErr != nil {
		//: InvalidAttachment.
		return typeErr
	}
	//: and a Content-ID must be usable inside a cid: URL.
	return validateContentID(field, a.ContentID)
}

// validateMediaType refuses a declared Content-Type that is not a plausible
// "type/subtype", leaving the full RFC 2045 §5.1 parameter grammar to the
// composer's mime.ParseMediaType call.
func validateMediaType(field, declared string) error {
	//: an empty type is legal here — the composer derives one.
	if declared == "" {
		//: derived at composition.
		return nil
	}
	//: the injection gate first: this string lands in Content-Type.
	if valueErr := ValidateHeaderValue(field+".ContentType", declared); valueErr != nil {
		//: HeaderInjection.
		return valueErr
	}
	//: one slash, non-empty on both sides — enough to catch "pdf" and
	//: "application/", the two ways this field is actually mistyped.
	slash := strings.IndexByte(declared, '/')
	//: a subtype must follow, and only one slash may separate them.
	if slash <= 0 || slash == len(declared)-1 {
		//: the media type is the caller's own literal, not attacker data.
		return errs.Wrap(InvalidAttachment, errs.WrapParams{},
			errs.String("field", field), errs.String("problem", "media type is not type/subtype"))
	}
	//: spellable.
	return nil
}

// validateContentID refuses a Content-ID that would not survive being written
// between angle brackets and referenced from a cid: URL.
func validateContentID(field, id string) error {
	//: an empty Content-ID means "not inline" and is the common case.
	if id == "" {
		//: a regular attachment.
		return nil
	}
	//: refused rather than trimmed: trimming would silently change which part
	//: the HTML's cid: reference resolves to.
	if strings.ContainsAny(id, "<>") {
		//: InvalidAttachment.
		return errs.Wrap(InvalidAttachment, errs.WrapParams{},
			errs.String("field", field), errs.String("problem", "content id carries angle brackets"))
	}
	//: RFC 2392 §2 borrows the addr-spec grammar for cid:, so the same guard
	//: applies to the value that resolves it.
	if idErr := ValidateAddress(field+".ContentID", AddressValue{Addr: id}); idErr != nil {
		//: relabel: the caller's problem is the attachment, not an address —
		//: but the address verdict stays reachable as a log-only field.
		return errs.Wrap(InvalidAttachment, errs.WrapParams{},
			errs.String("field", field), errs.String("problem", "content id is not an addr-spec"),
			errs.String("cause", idErr.Error()))
	}
	//: referenceable as cid:<id>.
	return nil
}
