// Package spool — declares the sentinel *errs.Error outcomes of the durable
// mail outbox. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
package spool

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78): the spool was wired wrong.
const exitConfig int = 78

// httpBadRequest is RFC 9110 400: the mail or the identifier the caller handed
// over is one no spool can keep, and resending it unchanged changes nothing.
const httpBadRequest int = 400

// httpUnavailable is RFC 9110 503: the spool is closed. Nothing is wrong with
// the request; this process will not take it.
const httpUnavailable int = 503

var (
	// SpoolMisconfigured refuses a spool that could never deliver.
	SpoolMisconfigured = errs.Define(CodeSpoolMisconfigured, "SPOOL_MISCONFIGURED",
		"The mail spool cannot run as configured",
		"service/app/mail/spool: New refused its Config, or Send got an identifier from Config.NewID that is empty, longer than MaxIDBytes or not an RFC 5322 dot-atom; the fields name the setting and the problem",
		errs.WithExitCode(exitConfig))

	// SpoolClosed refuses a Send or a SendWithID after Close.
	SpoolClosed = errs.Define(CodeSpoolClosed, "SPOOL_CLOSED",
		"The mail spool is closed",
		"service/app/mail/spool: Send or SendWithID was called after Close",
		errs.WithHTTPStatus(httpUnavailable))

	// MessageUndecodable reports a spooled record that does not decode. It is
	// the failure of that delivery, so the record is retried and then
	// dead-lettered with the rest of its bytes intact for an investigator.
	MessageUndecodable = errs.Define(CodeMessageUndecodable, "MESSAGE_UNDECODABLE",
		"A spooled mail could not be read back",
		"service/app/mail/spool: a record in the spool is not a spooled mail; the fields name the queue message")

	// MessageUnencodable refuses a mail Send could not write into the spool.
	// Nothing was queued.
	MessageUnencodable = errs.Define(CodeMessageUnencodable, "MESSAGE_UNENCODABLE",
		"The mail could not be written to the spool",
		"service/app/mail/spool: encoding/json refused the spooled record; a Date outside the years 0 to 9999 is the one way to get here",
		errs.WithHTTPStatus(httpBadRequest))

	// TransportPanicked is the failure of an attempt whose transport
	// panicked. The spool recovers it so the attempt fails like any other —
	// retried after its backoff, dead-lettered after the last — and so the
	// consumer and every other mail are untouched. The value and the stack
	// travel as fields, never in the Public text a mailbox may show.
	TransportPanicked = errs.Define(CodeTransportPanicked, "TRANSPORT_PANICKED",
		"The mail transport panicked and was recovered",
		"service/app/mail/spool: the transport panicked during an attempt; the fields carry the mail, the panic value and the stack")

	// InvalidMailID refuses an identifier SendWithID was given that no mail
	// can keep, because it becomes the left half of the mail's Message-ID.
	// Nothing was queued. The fields name the rule it broke and its length,
	// never the identifier, which may carry the very bytes it was refused for.
	InvalidMailID = errs.Define(CodeInvalidMailID, "INVALID_MAIL_ID",
		"The mail identifier is malformed",
		"service/app/mail/spool: SendWithID was given an identifier that is empty, longer than MaxIDBytes or not an RFC 5322 dot-atom; the fields name the problem and the length, never the identifier",
		errs.WithHTTPStatus(httpBadRequest))
)
