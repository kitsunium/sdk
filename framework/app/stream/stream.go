// Package stream plugs kit's streams in: the log a streamed topic (a
// topic made Streamed) appends each message to once, whatever its
// number of subscriptions, and the consumer groups its subscriptions read it
// as — the SDK's stream in memory, in the data directory or on a database. A
// product that declares a streamed topic imports it:
//
//	import _ "github.com/kitsunium/sdk/framework/app/stream"
//
// A product that declares none does not, and links none of the SDK's
// stream; a program that declares one without it is refused at the start,
// the refusal naming this import (CodeSubsystemMissing). It imports
// framework/app/store: a stream on a database joins the store subsystem's
// transactions. framework/app does not import it: kit gen writes the line
// for a designed product, and a hand-written one writes it.
package stream

import (
	"github.com/kitsunium/sdk/framework/internal/app/streamengine"
	// A stream on a database joins the store subsystem's transactions.
	_ "github.com/kitsunium/sdk/framework/app/store"
)

// Importing the package plugs the streams in: a blank package-level value
// rather than an init, the SDK's convention.
var _ = enable()

// enable plugs the streams, their consumer groups and the migration of a
// stream's tables into every app.
func enable() bool {
	streamengine.Enable()
	return true
}
