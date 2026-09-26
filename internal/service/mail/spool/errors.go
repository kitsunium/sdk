// Package spool — declares the sentinel *errs.Error outcomes. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
package spool

import (
	"net/http"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// exitConfig matches sysexits EX_CONFIG (78): the spool was wired wrong.
const exitConfig int = 78

var (
	// SpoolMisconfigured refuses a spool that could never deliver.
	SpoolMisconfigured = errs.Define(CodeSpoolMisconfigured, "SPOOL_MISCONFIGURED",
		"The mail spool cannot run as configured",
		"service/mail/spool: New refused its Config, or Send got an empty identifier from Config.NewID; the fields name the setting and the problem",
		errs.WithExitCode(exitConfig))

	// SpoolClosed refuses a Send after Close.
	SpoolClosed = errs.Define(CodeSpoolClosed, "SPOOL_CLOSED",
		"The mail spool is closed",
		"service/mail/spool: Send was called after Close",
		errs.WithHTTPStatus(http.StatusServiceUnavailable))

	// MessageUndecodable reports a spooled record that does not decode. It is
	// the failure of that delivery, so the record is retried and then
	// dead-lettered with the rest of its bytes intact for an investigator.
	MessageUndecodable = errs.Define(CodeMessageUndecodable, "MESSAGE_UNDECODABLE",
		"A spooled mail could not be read back",
		"service/mail/spool: a record in the spool is not a spooled mail; the fields name the queue message")

	// MessageUnencodable refuses a mail Send could not write into the spool.
	// Nothing was queued.
	MessageUnencodable = errs.Define(CodeMessageUnencodable, "MESSAGE_UNENCODABLE",
		"The mail could not be written to the spool",
		"service/mail/spool: encoding/json refused the spooled record; a Date outside the years 0 to 9999 is the one way to get here",
		errs.WithHTTPStatus(http.StatusBadRequest))

	// TransportPanicked is the failure of an attempt whose transport
	// panicked. The spool recovers it so the attempt fails like any other —
	// retried after its backoff, dead-lettered after the last — and so the
	// consumer and every other mail are untouched. The value and the stack
	// travel as fields, never in the Public text a mailbox may show.
	TransportPanicked = errs.Define(CodeTransportPanicked, "TRANSPORT_PANICKED",
		"The mail transport panicked and was recovered",
		"service/mail/spool: the transport panicked during an attempt; the fields carry the mail, the panic value and the stack")
)
