// Package queue plugs kit's queues in: the queues of the topics a service
// declares and of their subscriptions, of its watches and of its queued
// commands, and the outbox of a mailer on a database — the brokers the SDK's
// queue runs them on, in memory, in the data directory or on a database, and
// their consumers. A product that declares one imports it:
//
//	import _ "github.com/kitsunium/sdk/framework/kit/queue"
//
// A product that declares none does not, and links none of those brokers; a
// program that declares one without it is refused at the start, the refusal
// naming this import (CodeSubsystemMissing). It imports framework/kit/store:
// a queue on a database joins the store subsystem's transactions, and a
// sealed message is sealed under data keys kit keeps in a store of its own.
// framework/kit imports it too until ADR 0179's last step: no product needs
// the line yet.
package queue

import (
	"github.com/kitsunium/sdk/framework/internal/kit/queuekit"
	// A queue on a database joins the store subsystem's transactions, and a
	// sealed message needs kit's store of data keys (ADR 0179 Q4).
	_ "github.com/kitsunium/sdk/framework/kit/store"
)

// Importing the package plugs the queues in: a blank package-level value
// rather than an init, the SDK's convention.
var _ = enable()

// enable plugs the queues' brokers, their consumer and the migration of a
// queue's table into every app.
func enable() bool {
	queuekit.Enable()
	return true
}
