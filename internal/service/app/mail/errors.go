package mail

import (
	"errors"
	"net/textproto"

	coremail "github.com/kitsunium/sdk/internal/core/app/mail"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// wrapAs returns the given mail sentinel as the error origin — its code,
// reason and public message win — attaching the cause's message and any extra
// fields as structured metadata.
//
// Wrapping the sentinel rather than the cause is what keeps this domain's code
// intact: errs.Wrap is origin-wins, so passing an *errs.Error cause first would
// let it hijack the verdict. A nil cause yields the sentinel with only the
// caller's fields.
func wrapAs(sentinel *errs.Error, cause error, fields ...errs.FieldValue) error {
	//: a nil cause contributes no field — keep only what the caller supplied.
	if cause == nil {
		//: wrap for the fields alone.
		return errs.Wrap(sentinel, errs.WrapParams{}, fields...)
	}
	//: carry the cause message as a log-only field so it stays diagnosable.
	return errs.Wrap(sentinel, errs.WrapParams{}, append(fields, errs.String("cause", cause.Error()))...)
}

// refused returns SendRefused for a MAIL, RCPT or DATA that failed. When the
// server answered with a reply, the reply is the error's source: errors.As
// finds it as a *net/textproto.Error, whose Code tells a permanent 5xx from a
// temporary 4xx — what the mail spool reads to dead-letter a mail at once
// rather than repeat a command RFC 5321 §4.2.1 says not to repeat. SendRefused
// stays the origin, its Public unchanged, and the reply's text stays a
// log-only field. A failure without a reply — a write cut mid-message — is
// SendRefused as wrapAs builds it.
//
// Only the transaction's commands keep their reply reachable: a 5xx to the
// greeting, EHLO, STARTTLS or AUTH speaks of the relay or of this program's
// configuration, never of the mail, and a spool that dead-lettered every mail
// for it would lose them all to one misconfiguration.
func refused(cause error, fields ...errs.FieldValue) error {
	//: an SDK error would win the origin from SendRefused: its text, as ever.
	if inner, sdk := errors.AsType[*errs.Error](cause); sdk {
		//: the sentinel, the cause's text as a field.
		return wrapAs(coremail.SendRefused, inner, fields...)
	}
	reply, replied := errors.AsType[*textproto.Error](cause)
	//: no reply to keep — a write cut mid-message.
	if !replied {
		//: the sentinel, the cause's text as a field.
		return wrapAs(coremail.SendRefused, cause, fields...)
	}
	sentinel := coremail.SendRefused
	//: the reply is not an SDK error, so these params are the origin and the
	//: reply the source; its code is a field too, for a log line.
	return errs.Wrap(cause, errs.WrapParams{
		Code: sentinel.Code(), Reason: sentinel.Reason(), Public: sentinel.Public(),
		Private: sentinel.Private(), ExitCode: sentinel.ExitCode(),
	}, append(fields, errs.Int("reply", reply.Code), errs.String("cause", cause.Error()))...)
}
