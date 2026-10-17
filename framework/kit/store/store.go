// Package store plugs kit's stores in: the stores a service declares, the
// databases a product keeps them in and their migrations, kit.Transact on a
// database, sealing at rest, history, revisions, trash, retention, holds,
// the privacy journal, Records and `data copy` — the engines the SDK's
// document store and sql run them on. A product that declares a store or a
// database imports it:
//
//	import _ "github.com/kitsunium/sdk/framework/kit/store"
//
// A product that declares none does not, and links none of those engines; a
// program that declares one without it is refused at the start, the refusal
// naming this import (CodeSubsystemMissing). A connector —
// framework/connectors/postgres, …/mysql, …/sqlite — imports it itself, and
// framework/kit does too until ADR 0179's last step: no product needs the
// line yet.
package store

import "github.com/kitsunium/sdk/framework/internal/kit/storekit"

// Importing the package plugs the stores in: a blank package-level value
// rather than an init, the SDK's convention.
var _ = enable()

// enable plugs the stores' engines, kit's scope on a database and the
// migrations of kit's tables into every app.
func enable() bool {
	storekit.Enable()
	return true
}
