// Package mail — the message as a value, and the envelope derived from it.
package mail

import "time"

// MessageValue is one mail, as a value. It is deliberately a struct and not an
// interface: a message has no behaviour worth abstracting, it is data, and a
// value can be built in a literal, compared field by field in a test, and
// carried across a goroutine without a lock.
//
// Nothing here is a wire format. The MIME structure the fields imply is chosen
// by the composer in internal/service/app/mail, and which parts appear is a pure
// function of which fields are populated — see that package's CLAUDE.md for
// the table.
//
// Every string field that reaches a header is validated by the guards in
// internal/service/app/mail, which every transport runs before composing. The
// slices are never mutated by this package.
type MessageValue struct {
	// Date is the origination date (RFC 5322 §3.6.1), which is REQUIRED in a
	// message. A zero Date is stamped by the composer from its clock rather
	// than refused: unlike a file mode, there is exactly one defensible value
	// and it is "now" (ADR 0031's clamping half).
	Date time.Time
	// From is the author mailbox (RFC 5322 §3.6.2). Exactly one is supported;
	// the multi-author form requires a Sender header to disambiguate and no
	// caller has ever wanted it by accident.
	From AddressValue
	// Subject is the unstructured subject field (RFC 5322 §3.6.5). Non-ASCII
	// content is encoded per RFC 2047 at composition; a CR or LF here is
	// [HeaderInjection].
	Subject string
	// MessageID is the optional Message-ID (RFC 5322 §3.6.4), angle brackets
	// included or omitted at the caller's choice — the composer adds them.
	//
	// An empty MessageID emits NO header. RFC 6409 §8.2 makes adding one the
	// submission server's job when it is absent, and generating one here would
	// mean inventing both entropy and a domain the SDK does not own.
	MessageID string
	// ReplyTo is the optional Reply-To mailbox list (RFC 5322 §3.6.2).
	ReplyTo []AddressValue
	// To is the primary recipient list (RFC 5322 §3.6.3).
	To []AddressValue
	// Cc is the carbon-copy recipient list (RFC 5322 §3.6.3).
	Cc []AddressValue
	// Bcc is the blind carbon-copy list. It reaches the ENVELOPE — the
	// [EnvelopeValue] internal/service/app/mail derives — and never a header.
	Bcc []AddressValue
	// Headers are additional fields, emitted in slice order so a composed
	// message is byte-deterministic. A name the composer owns is
	// [ReservedHeader].
	Headers []HeaderFieldValue
	// Text is the text/plain body.
	Text string
	// HTML is the text/html body. With Text it becomes a multipart/alternative
	// in which the plain part comes FIRST — RFC 2046 §5.1.4 orders alternatives
	// by increasing preference, so the last one is the one a client shows.
	HTML string
	// Attachments are the file parts. One carrying a ContentID is INLINE and is
	// referenced from the HTML body as cid:<ContentID> (RFC 2392 §2).
	Attachments []AttachmentValue
}
