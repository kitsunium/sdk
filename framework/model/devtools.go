// What the Studio shows beside a request in dev: log records, the captured
// mails, and a test's mocks.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

// Mail statuses.
const (
	MailQueued   string = core.MailQueued   // in the outbox, not yet handed to the transport
	MailSent     string = core.MailSent     // the transport accepted it (capture: kept for the mailbox)
	MailRetrying string = core.MailRetrying // the last attempt failed; another is scheduled
	MailDead     string = core.MailDead     // abandoned after its last attempt
)

const (
	// MockReplace is the one mode a [Mock] has: a test's kit.Replace, a typed
	// function run in place of an operation's handler — or of what a port calls —
	// while the rest of its pipeline still runs. The Studio sets none (D13): it
	// shows the application, it does not act on it, so the respond, fail and
	// delay modes of the platform's kit have no representation here.
	MockReplace string = core.MockReplace
)

type (
	// MailSummary is the name [MailMessage] embeds its summary under: the field
	// a caller sets and reads is MailMessage.MailSummary, its JSON members
	// promoted beside the body's.
	MailSummary = core.MailSummary
)

type (
	// MailMessage is a whole captured mail, for the Studio's mailbox.
	// It is kept by the capture transport of dev and tests only, never in
	// production.
	MailMessage = core.MailMessage
)
