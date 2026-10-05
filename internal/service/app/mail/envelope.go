package mail

import coremail "github.com/kitsunium/sdk/internal/core/app/mail"

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
func Envelope(m coremail.MessageValue) (envelope coremail.EnvelopeValue, err error) {
	//: validation first: an envelope built from an unvalidated message is a
	//: set of strings that may each contain a CRLF, which net/smtp would then
	//: refuse one command at a time, halfway through a session.
	if validationErr := Validate(m); validationErr != nil {
		//: the message's own verdict, unchanged.
		return coremail.EnvelopeValue{}, validationErr
	}
	//: exactly as many entries as there are recipients, allocated once.
	recipients := make([]string, 0, len(m.To)+len(m.Cc)+len(m.Bcc))
	//: To, then Cc, then Bcc — deterministic, and Bcc last so a truncated log
	//: of the first commands does not disclose the blind recipients.
	for _, group := range [][]coremail.AddressValue{m.To, m.Cc, m.Bcc} {
		//: every address in this group, in order.
		for _, addr := range group {
			//: the addr-spec alone; a display name is header material and has
			//: no meaning in RCPT TO.
			recipients = append(recipients, addr.Addr)
		}
	}
	//: the return path is the author's mailbox.
	return coremail.EnvelopeValue{From: m.From.Addr, To: recipients}, nil
}
