// Package mail — the composer: a message value in, RFC 5322 wire bytes out.
package mail

import (
	"bytes"
	"crypto/rand"
	"io"
	"time"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// Composer turns a [coremail.MessageValue] into the wire bytes a transport
// sends. It is safe for concurrent use and holds no state between messages.
type Composer struct {
	clock  clock.Clock
	random io.Reader
}

// NewComposer returns a composer wired from cfg.
func NewComposer(cfg ComposerConfig) *Composer {
	composer := &Composer{clock: cfg.Clock, random: cfg.Rand}
	//: the system clock is the only defensible "now".
	if composer.clock == nil {
		//: clamped, not refused.
		composer.clock = clock.System
	}
	//: and crypto/rand the only defensible source of a boundary.
	if composer.random == nil {
		//: clamped, not refused.
		composer.random = rand.Reader
	}
	//: ready.
	return composer
}

// Compose validates msg and renders it as one RFC 5322 message: the header
// block, an empty line, and the MIME body.
//
// The MIME structure is a pure function of which fields are populated, and
// picking it wrongly is how a message renders as raw HTML in one client and as
// an empty body in another:
//
//	text only                      → text/plain
//	HTML only                      → text/html
//	text + HTML                    → multipart/alternative(plain, html)
//	body + inline parts            → multipart/related(body, inline…)
//	body + attachments             → multipart/mixed(body, attachment…)
//	body + inline + attachments    → multipart/mixed(multipart/related(…), attachment…)
//
// No container is emitted for a single child. A multipart/alternative with one
// alternative and a multipart/mixed with one part are both legal and both are
// noise, and the second one makes some clients show a paperclip on a message
// with no attachment.
//
// Inside multipart/alternative the PLAIN part comes first. RFC 2046 §5.1.4
// orders alternatives "in increasing order of preference" and tells a receiver
// to display the last one it understands — so a composer that writes HTML
// first has just asked every client to show the plain text.
func (c *Composer) Compose(msg coremail.MessageValue) (raw []byte, err error) {
	//: the whole-message gate first: nothing is rendered from a message that
	//: will be refused, so a header-injection attempt costs no allocation.
	if validationErr := coremail.Validate(msg); validationErr != nil {
		//: the core verdict, unchanged.
		return nil, validationErr
	}
	builder, builderErr := c.newComposition()
	//: ComposeFailed.
	if builderErr != nil {
		//: no randomness, no boundaries, no message.
		return nil, builderErr
	}
	body, structureErr := builder.buildBody(msg)
	//: ComposeFailed.
	if structureErr != nil {
		//: refused before any byte was written.
		return nil, structureErr
	}
	var buf bytes.Buffer
	//: one allocation instead of the doubling growth measured at 43.7 % of all
	//: bytes allocated by a compose — see estimateSize.
	buf.Grow(estimateSize(msg))
	//: the message header block: originator, destination, subject, date.
	if headerErr := c.writeMessageHeaders(&buf, msg); headerErr != nil {
		//: HeaderInjection, HeaderTooLong or ReservedHeader.
		return nil, headerErr
	}
	//: the root entity's own MIME headers share the same block.
	if entityErr := body.writeHeaders(&buf); entityErr != nil {
		//: HeaderInjection or HeaderTooLong.
		return nil, entityErr
	}
	//: the empty line that separates headers from body (RFC 5322 §2.1).
	buf.WriteString(crlf)
	//: and the body itself.
	if bodyErr := body.writeBody(&buf); bodyErr != nil {
		//: wrapped so a caller can tell a composition defect from a refusal.
		return nil, wrapAs(ComposeFailed, bodyErr)
	}
	//: the complete message.
	return buf.Bytes(), nil
}

// writeMessageHeaders emits the RFC 5322 header block, in a fixed order.
//
// Bcc is absent, and its absence is the domain's headline decision: the blind
// recipients reach RCPT TO through [coremail.MessageValue.Envelope] and no
// header names them. See that method for why the other two treatments
// RFC 5322 §3.6.3 permits are not taken.
func (c *Composer) writeMessageHeaders(dst io.Writer, msg coremail.MessageValue) error {
	//: the originator field, always present (RFC 5322 §3.6.2).
	fields := []coremail.HeaderFieldValue{{Name: coremail.HeaderFrom, Value: formatAddress(msg.From)}}
	//: the destination fields, each omitted when its list is empty rather than
	//: emitted empty — an empty To is a field no receiver knows what to do with.
	fields = appendAddressFields(fields, msg)
	//: the subject, RFC 2047 encoded only if it needs to be.
	if msg.Subject != "" {
		//: RFC 5322 §3.6.5.
		fields = append(fields, coremail.HeaderFieldValue{
			Name: coremail.HeaderSubject, Value: encodeUnstructured(msg.Subject),
		})
	}
	//: the origination date is REQUIRED (RFC 5322 §3.6.1), so a zero one is
	//: stamped rather than omitted.
	fields = append(fields, coremail.HeaderFieldValue{Name: coremail.HeaderDate, Value: c.dateValue(msg)})
	//: a caller-supplied Message-ID, bracketed here so the caller need not.
	if msg.MessageID != "" {
		//: RFC 5322 §3.6.4; absent means the submission server adds one
		//: (RFC 6409 §8.2) rather than this SDK inventing a domain.
		fields = append(fields, coremail.HeaderFieldValue{
			Name: coremail.HeaderMessageID, Value: bracket(msg.MessageID),
		})
	}
	//: then the caller's own extra fields, in slice order.
	fields = append(fields, msg.Headers...)
	//: and finally the MIME declaration, which RFC 2045 §4 puts on the MESSAGE
	//: and not on a part.
	fields = append(fields, coremail.HeaderFieldValue{Name: coremail.HeaderMIMEVersion, Value: mimeVersion})
	//: one gate, one fold, one write per field.
	for _, field := range fields {
		//: writeHeader re-runs the injection gate at the point of writing.
		if writeErr := writeHeader(dst, field.Name, field.Value); writeErr != nil {
			//: HeaderInjection or HeaderTooLong.
			return writeErr
		}
	}
	//: the block is complete apart from the entity's own Content-* fields.
	return nil
}

// appendAddressFields adds To, Cc and Reply-To, skipping every list the caller
// left empty. Bcc is deliberately absent.
func appendAddressFields(
	fields []coremail.HeaderFieldValue, msg coremail.MessageValue,
) []coremail.HeaderFieldValue {
	lists := []addressField{
		{coremail.HeaderTo, msg.To},
		{coremail.HeaderCc, msg.Cc},
		{coremail.HeaderReplyTo, msg.ReplyTo},
	}
	//: each in declaration order, so the block is byte-deterministic.
	for _, list := range lists {
		//: present only when populated.
		if len(list.addrs) > 0 {
			//: rendered as a mailbox-list (RFC 5322 §3.4).
			fields = append(fields, coremail.HeaderFieldValue{
				Name: list.name, Value: formatAddressList(list.addrs),
			})
		}
	}
	//: the block so far.
	return fields
}

// dateValue renders the origination date in the RFC 5322 §3.3 date-time form.
func (c *Composer) dateValue(msg coremail.MessageValue) string {
	stamp := msg.Date
	//: a zero Date is an unfilled field, and "now" is the only value it could
	//: have meant — unlike a file mode, there is nothing to guess between.
	if stamp.IsZero() {
		//: from the composer's clock, so a test can pin it.
		stamp = c.clock.Now()
	}
	//: time.RFC1123Z is RFC 5322 §3.3's date-time with a numeric zone, which
	//: §4.3 prefers over the obsolete alphabetic ones.
	return stamp.Format(time.RFC1123Z)
}
