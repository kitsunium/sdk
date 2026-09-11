// Package mail — the message as a value, and the envelope derived from it.
package mail

import "time"

// MessageValue is one mail, as a value. It is deliberately a struct and not an
// interface: a message has no behaviour worth abstracting, it is data, and a
// value can be built in a literal, compared field by field in a test, and
// carried across a goroutine without a lock.
//
// Nothing here is a wire format. The MIME structure the fields imply is chosen
// by the composer in internal/service/mail, and which parts appear is a pure
// function of which fields are populated — see that package's CLAUDE.md for
// the table.
//
// Every string field that reaches a header is validated by [Validate]. The
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
	// Bcc is the blind carbon-copy list. It reaches the ENVELOPE and never a
	// header — see [MessageValue.Envelope].
	Bcc []AddressValue
	// Headers are additional fields, emitted in slice order so a composed
	// message is byte-deterministic. A name the composer owns is
	// [ReservedHeader]; see [IsReservedHeader].
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

// Envelope derives the SMTP envelope from the message, after validating it.
//
// Bcc is the point. RFC 5322 §3.6.3 describes three permissible treatments of
// a Bcc field, and this domain takes the one that cannot leak: the addresses
// reach RCPT TO and the header is never written. The other two — sending each
// Bcc recipient their own copy carrying only their own Bcc line, or sending the
// header intact to everyone — are either N messages the caller did not ask for
// or a disclosure RFC 5321 §7.2 warns about explicitly.
//
// The recipient order is To, then Cc, then Bcc, and it is deterministic so a
// test can assert on it. Duplicates are NOT removed: an address listed in both
// To and Cc is two RCPT TO commands, which every MTA collapses into one
// delivery, and removing them here would mean deciding that two addresses that
// differ only in case are the same mailbox — a judgement only the receiving
// domain is entitled to make.
func (m MessageValue) Envelope() (envelope EnvelopeValue, err error) {
	//: validation first: an envelope built from an unvalidated message is a
	//: set of strings that may each contain a CRLF, which net/smtp would then
	//: refuse one command at a time, halfway through a session.
	if validationErr := Validate(m); validationErr != nil {
		//: the message's own verdict, unchanged.
		return EnvelopeValue{}, validationErr
	}
	//: exactly as many entries as there are recipients, allocated once.
	recipients := make([]string, 0, len(m.To)+len(m.Cc)+len(m.Bcc))
	//: To, then Cc, then Bcc — deterministic, and Bcc last so a truncated log
	//: of the first commands does not disclose the blind recipients.
	for _, group := range [][]AddressValue{m.To, m.Cc, m.Bcc} {
		//: every address in this group, in order.
		for _, addr := range group {
			//: the addr-spec alone; a display name is header material and has
			//: no meaning in RCPT TO.
			recipients = append(recipients, addr.Addr)
		}
	}
	//: the return path is the author's mailbox.
	return EnvelopeValue{From: m.From.Addr, To: recipients}, nil
}
