// Package mail plugs kit's mailers in: the outbox a mailer keeps — the SDK's
// mail spool, sealed on the database that keeps the mailer's service, else in
// the data directory or in memory — and the transports that empty it, the
// SDK's SMTP transport and its capture transport. A product that declares a
// mailer imports it:
//
//	import _ "github.com/kitsunium/sdk/framework/kit/mail"
//
// A product that declares none does not, and links none of that outbox; a
// program that declares one without it is refused at the start, the refusal
// naming this import (CodeSubsystemMissing). It imports framework/kit/store
// and framework/kit/queue: a mailer's outbox on a database is the queue
// subsystem's, joined to the transaction of the database the store subsystem
// opened, and sealed under kit's data keys, kept in a store of kit's own.
// framework/kit imports it too until ADR 0179's last step: no product needs
// the line yet.
package mail

import (
	"github.com/kitsunium/sdk/framework/internal/kit/mailkit"
	// A mailer's outbox on a database is a queue of the queue subsystem,
	// on a database of the store subsystem's (ADR 0179 Q4); the queue
	// package imports the store package.
	_ "github.com/kitsunium/sdk/framework/kit/queue"
	_ "github.com/kitsunium/sdk/framework/kit/store"
)

// Importing the package plugs the mailers in: a blank package-level value
// rather than an init, the SDK's convention.
var _ = enable()

// enable plugs the mailers' outbox and transports into every app.
func enable() bool {
	mailkit.Enable()
	return true
}
