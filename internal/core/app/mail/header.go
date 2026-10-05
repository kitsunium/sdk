package mail

// Header field names the composer owns. A guard compares them
// case-insensitively, because RFC 5322 makes a field name case-insensitive
// (the field-name production) and an attacker who could smuggle "bCc" past a
// case-sensitive check would have smuggled Bcc. The guards — the reserved set
// and the injection gate every writer runs — are internal/service/app/mail's:
// the names are vocabulary, checking a field is a mechanism (ADR 0160).
const (
	// HeaderFrom is RFC 5322 §3.6.2's originator field.
	HeaderFrom string = "From"
	// HeaderTo is the primary recipient field, RFC 5322 §3.6.3.
	HeaderTo string = "To"
	// HeaderCc is the carbon-copy field, RFC 5322 §3.6.3.
	HeaderCc string = "Cc"
	// HeaderBcc is the blind carbon-copy field. This domain never emits it:
	// a Bcc recipient reaches the [EnvelopeValue] and no header.
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
