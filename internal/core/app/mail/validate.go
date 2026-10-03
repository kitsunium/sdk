// Package mail — the one whole-message guard both transports run.
package mail

import (
	"strconv"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// addressGroup pairs a header name with the list emitted under it, so the
// refusal a caller reads names the position in the message rather than the
// position in a loop.
type addressGroup struct {
	field string
	addrs []AddressValue
}

// Validate reports whether m can be composed and sent, and returns the first
// typed refusal it finds.
//
// It is exported and lives in core rather than beside the composer because
// BOTH transports run it, and that is what makes the in-memory one a faithful
// double: a message the SMTP transport would refuse is refused identically by
// the memory transport, in a test, before anybody deploys.
//
// It stops at the FIRST problem rather than collecting every one. That is the
// opposite of what the validation domain does (ADR 0046) and it is deliberate:
// a validation report describes a form a human will fix field by field, while
// this is a security gate, and a gate that keeps evaluating a message it has
// already decided to refuse is a gate doing work an attacker chose for it.
func Validate(m MessageValue) error {
	//: the originator field is mandatory (RFC 5322 §3.6) and has no default.
	if m.From.IsZero() {
		//: MissingSender, before anything else is judged.
		return errs.Wrap(MissingSender, errs.WrapParams{})
	}
	//: every address that will reach a header or a RCPT TO.
	if addrErr := validateAllAddresses(m); addrErr != nil {
		//: InvalidAddress, UnsupportedAddress or HeaderInjection.
		return addrErr
	}
	//: the subject is unstructured text and faces the injection gate.
	if subjectErr := ValidateHeaderValue(HeaderSubject, m.Subject); subjectErr != nil {
		//: HeaderInjection.
		return subjectErr
	}
	//: a caller-supplied Message-ID is written between angle brackets and is
	//: therefore header material too.
	if idErr := ValidateHeaderValue(HeaderMessageID, m.MessageID); idErr != nil {
		//: HeaderInjection.
		return idErr
	}
	//: extra headers: name grammar, reserved-name refusal, value gate.
	for _, field := range m.Headers {
		//: one verdict per field, first one wins.
		if fieldErr := ValidateHeader(field); fieldErr != nil {
			//: HeaderInjection or ReservedHeader.
			return fieldErr
		}
	}
	//: content last, because a message with a perfect body and a poisoned
	//: header is still a refusal and the header is the dangerous half.
	return validateContent(m)
}

// validateAllAddresses runs [ValidateAddress] over From and every list, naming
// each position so a caller with fifty recipients learns which one is wrong.
func validateAllAddresses(m MessageValue) error {
	//: the author first.
	if fromErr := ValidateAddress(HeaderFrom, m.From); fromErr != nil {
		//: InvalidAddress or UnsupportedAddress.
		return fromErr
	}
	//: the four lists, each under its own header name so the position in the
	//: error is the position in the message.
	lists := []addressGroup{
		{HeaderTo, m.To},
		{HeaderCc, m.Cc},
		{HeaderBcc, m.Bcc},
		{HeaderReplyTo, m.ReplyTo},
	}
	//: every list, every entry, in declaration order.
	for _, list := range lists {
		//: index included, because "To[37]" is actionable and "To" is not.
		for index, addr := range list.addrs {
			//: one verdict per address, first one wins.
			if addrErr := ValidateAddress(list.field+"["+strconv.Itoa(index)+"]", addr); addrErr != nil {
				//: InvalidAddress, UnsupportedAddress or HeaderInjection.
				return addrErr
			}
		}
	}
	//: a message with at least one deliverable destination.
	if len(m.To)+len(m.Cc)+len(m.Bcc) == 0 {
		//: NoRecipients — refused here, not by the server after a dial.
		return errs.Wrap(NoRecipients, errs.WrapParams{})
	}
	//: every mailbox is carriable.
	return nil
}

// validateContent refuses an empty message and checks every attachment,
// including the one structural rule an inline part depends on.
func validateContent(m MessageValue) error {
	//: a message with nothing in it is an unfilled struct (ADR 0031).
	if m.Text == "" && m.HTML == "" && len(m.Attachments) == 0 {
		//: EmptyBody.
		return errs.Wrap(EmptyBody, errs.WrapParams{})
	}
	//: whether a cid: reference has anywhere to be written from.
	hasBody := m.Text != "" || m.HTML != ""
	//: every attachment, position named.
	for index := range m.Attachments {
		//: one verdict per part, first one wins.
		if partErr := validateOnePart(&m.Attachments[index], index, hasBody); partErr != nil {
			//: InvalidAttachment or HeaderInjection.
			return partErr
		}
	}
	//: composable.
	return nil
}

// validateOnePart runs the per-attachment guards and the one structural rule
// an inline part depends on.
func validateOnePart(attachment *AttachmentValue, index int, hasBody bool) error {
	field := "Attachments[" + strconv.Itoa(index) + "]"
	//: the per-attachment guards.
	if attachmentErr := ValidateAttachment(field, attachment); attachmentErr != nil {
		//: InvalidAttachment or HeaderInjection.
		return attachmentErr
	}
	//: an inline part with no body is a multipart/related with no root
	//: (RFC 2387 §3.1), which is not a structure this domain can emit.
	if attachment.Inline() && !hasBody {
		//: refused, rather than demoted to a regular attachment — that would
		//: silently turn an embedded image into a file to download.
		return errs.Wrap(InvalidAttachment, errs.WrapParams{},
			errs.String("field", field),
			errs.String("problem", "inline part in a message with no body to reference it"))
	}
	//: renderable.
	return nil
}
