// Package mail — the header grammar, and the injection gate every writer in
// this domain runs before a byte reaches a buffer.
package mail

import (
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Header field names the composer owns. They are compared case-insensitively,
// because RFC 5322 makes a field name case-insensitive (the field-name
// production) and an attacker who could smuggle "bCc" past a case-sensitive
// check would have smuggled Bcc.
const (
	// HeaderFrom is RFC 5322 §3.6.2's originator field.
	HeaderFrom string = "From"
	// HeaderTo is the primary recipient field, RFC 5322 §3.6.3.
	HeaderTo string = "To"
	// HeaderCc is the carbon-copy field, RFC 5322 §3.6.3.
	HeaderCc string = "Cc"
	// HeaderBcc is the blind carbon-copy field. This domain never emits it —
	// see [MessageValue.Envelope].
	HeaderBcc string = "Bcc"
	// HeaderReplyTo is RFC 5322 §3.6.2's Reply-To field.
	HeaderReplyTo string = "Reply-To"
	// HeaderSubject is RFC 5322 §3.6.5's subject field.
	HeaderSubject string = "Subject"
	// HeaderDate is RFC 5322 §3.6.1's origination date field.
	HeaderDate string = "Date"
	// HeaderMessageID is RFC 5322 §3.6.4's identification field.
	HeaderMessageID string = "Message-ID"
	// HeaderMIMEVersion is RFC 2045 §4's version field.
	HeaderMIMEVersion string = "MIME-Version"
	// HeaderContentType is RFC 2045 §5's media type field.
	HeaderContentType string = "Content-Type"
	// HeaderContentTransferEncoding is RFC 2045 §6's encoding field.
	HeaderContentTransferEncoding string = "Content-Transfer-Encoding"
	// HeaderContentDisposition is RFC 2183's disposition field.
	HeaderContentDisposition string = "Content-Disposition"
	// HeaderContentID is RFC 2045 §7's part identifier, referenced by cid:
	// URLs per RFC 2392 §2.
	HeaderContentID string = "Content-ID"
)

// forbiddenHeaderOctets are the three octets a header value may never carry.
// CR and LF end a field; NUL terminates the value for any consumer that
// reaches C and is never meaningful in a header.
const forbiddenHeaderOctets string = "\r\n\x00"

// reservedHeaders is the set of lower-cased names the composer emits itself. A
// caller-supplied field with one of these names is [ReservedHeader].
//
// The membership is not a style preference. RFC 5322 §3.6 permits exactly one
// From, Date, Subject and Message-ID per message, so a duplicate leaves every
// receiver free to believe a different one — and "Bcc" in this list is the
// header-injection attack arriving through a supported API instead of through
// a CRLF.
var reservedHeaders = map[string]struct{}{
	strings.ToLower(HeaderFrom):                    {},
	strings.ToLower(HeaderTo):                      {},
	strings.ToLower(HeaderCc):                      {},
	strings.ToLower(HeaderBcc):                     {},
	strings.ToLower(HeaderReplyTo):                 {},
	strings.ToLower(HeaderSubject):                 {},
	strings.ToLower(HeaderDate):                    {},
	strings.ToLower(HeaderMessageID):               {},
	strings.ToLower(HeaderMIMEVersion):             {},
	strings.ToLower(HeaderContentType):             {},
	strings.ToLower(HeaderContentTransferEncoding): {},
	strings.ToLower(HeaderContentDisposition):      {},
	strings.ToLower(HeaderContentID):               {},
}

// IsReservedHeader reports whether name is a field the composer emits itself,
// comparing case-insensitively, as RFC 5322 requires of a field name.
func IsReservedHeader(name string) bool {
	//: the grammar is case-insensitive, so the guard must be too — "bCc" is Bcc.
	_, reserved := reservedHeaders[strings.ToLower(strings.TrimSpace(name))]
	//: reserved names belong to the composer alone.
	return reserved
}

// ValidateHeaderName reports whether name is a usable field name and returns a
// typed refusal when it is not.
//
// RFC 5322 §3.6.8 defines a field name as one or more printable US-ASCII
// characters other than the colon. Anything else — a space, a control byte, a
// non-ASCII rune, an embedded colon — would produce a line no receiver parses
// as the caller intended, and an embedded CRLF would produce two fields.
func ValidateHeaderName(name string) error {
	//: an empty name emits ": value", which is not a header at all.
	if name == "" {
		//: no field name to report, and none to leak.
		return errs.Wrap(HeaderInjection, errs.WrapParams{}, errs.String("header", "(empty)"))
	}
	//: byte-wise, because the grammar is octet-based and not rune-based.
	for index := range len(name) {
		//: printable US-ASCII, colon excluded (the ftext production).
		if name[index] < minPrintableASCII || name[index] > maxPrintableASCII || name[index] == ':' {
			//: the NAME is safe to report — it is what the caller must fix.
			return errs.Wrap(HeaderInjection, errs.WrapParams{},
				errs.String("header", name), errs.Int("offset", index))
		}
	}
	//: a usable field name.
	return nil
}

// ValidateHeaderValue reports whether value may be emitted under the field
// called name, and returns [HeaderInjection] when it may not.
//
// This is the gate the whole domain rests on. A header field ends at CRLF
// (RFC 5322 §2.2), so a CR or an LF inside a value does not corrupt the field
// — it TERMINATES it, and every octet after it becomes new header lines. A
// subject of "Hi\r\nBcc: attacker@example.com" is not a malformed subject; it
// is a well-formed subject followed by a well-formed blind-copy field, and the
// message is delivered to a party the sender never saw.
//
// A bare CR and a bare LF are refused as well as the pair, because receivers
// disagree about them — some normalise a lone LF to CRLF, which reconstructs
// the attack downstream from an input that looked survivable here.
//
// The refusal names the FIELD and never the value. The value is, by
// construction, the string an attacker chose, and a Public message travels
// places the SDK does not control.
func ValidateHeaderValue(name, value string) error {
	//: one pass, three forbidden octets — the CRLF pair needs no special case
	//: because either half alone is already fatal.
	if index := strings.IndexAny(value, forbiddenHeaderOctets); index >= 0 {
		//: name and offset are diagnostic; the value stays out of the error.
		return errs.Wrap(HeaderInjection, errs.WrapParams{},
			errs.String("header", name), errs.Int("offset", index))
	}
	//: emittable as one field.
	return nil
}

// ValidateHeader runs [ValidateHeaderName] and [ValidateHeaderValue] over one
// caller-supplied field and refuses a name the composer owns.
func ValidateHeader(field HeaderFieldValue) error {
	//: a malformed name is reported before anything is judged about the value.
	if nameErr := ValidateHeaderName(field.Name); nameErr != nil {
		//: HeaderInjection.
		return nameErr
	}
	//: a composer-owned name is refused even when perfectly well formed.
	if IsReservedHeader(field.Name) {
		//: the name is the caller's own literal and is safe to report.
		return errs.Wrap(ReservedHeader, errs.WrapParams{}, errs.String("header", field.Name))
	}
	//: and finally the value.
	return ValidateHeaderValue(field.Name, field.Value)
}
