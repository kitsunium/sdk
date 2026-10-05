// What the Studio shows beside a request in dev: log records, the captured
// mails, and a test's mocks.

package core

// Mail statuses.
const (
	MailQueued   = "queued"   // in the outbox, not yet handed to the transport
	MailSent     = "sent"     // the transport accepted it (capture: kept for the mailbox)
	MailRetrying = "retrying" // the last attempt failed; another is scheduled
	MailDead     = "dead"     // abandoned after its last attempt
)

// MockReplace is the one mode a [MockMessage] has: a test's kit.Replace, a typed
// function run in place of an operation's handler — or of what a port calls —
// while the rest of its pipeline still runs. The Studio sets none (D13): it
// shows the application, it does not act on it, so the respond, fail and
// delay modes of the platform's kit have no representation here.
const MockReplace = "replace"

// MailSummary is the name [MailMessage] embeds its summary under: the field
// a caller sets and reads is MailMessage.MailSummary, its JSON members
// promoted beside the body's.
type MailSummary = MailSummaryMessage

// MailMessage is a whole captured mail, for the Studio's mailbox.
// It is kept by the capture transport of dev and tests only, never in
// production.
type MailMessage struct {
	MailSummary
	// Text is the plain text body.
	Text string `json:"text,omitempty"`
	// HTML is the HTML body. The Studio renders it in a sandboxed frame
	// that runs no script.
	HTML string `json:"html,omitempty"`
	// Headers are the extra headers, rendered.
	Headers map[string]string `json:"headers,omitempty"`
	// Raw is the composed message, as the transport received it.
	Raw string `json:"raw,omitempty"`
}
