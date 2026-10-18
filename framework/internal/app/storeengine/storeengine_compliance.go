package storeengine

import "github.com/kitsunium/sdk/framework/internal/app/plug"

// : Asserts at compile time that the engines, the scope on a database and
// its migrator are what plug describes, with every sibling kit's core asks
// for.
var (
	_ plug.StoreEngine     = (*fileEngine)(nil)
	_ plug.Folder          = (*fileEngine)(nil)
	_ plug.Keyser          = (*fileEngine)(nil)
	_ plug.EntrySource     = (*fileEngine)(nil)
	_ plug.VersionKeeper   = (*fileEngine)(nil)
	_ plug.VersionWeighter = (*fileEngine)(nil)
	_ plug.Trasher         = (*fileEngine)(nil)
	_ plug.DocumentSource  = (*fileEngine)(nil)
	_ plug.Documents       = (*fileEngine)(nil)
	_ plug.DocumentSink    = (*fileEngine)(nil)

	_ plug.StoreEngine     = (*sqlEngine)(nil)
	_ plug.Keyser          = (*sqlEngine)(nil)
	_ plug.EntrySource     = (*sqlEngine)(nil)
	_ plug.Reindexer       = (*sqlEngine)(nil)
	_ plug.VersionKeeper   = (*sqlEngine)(nil)
	_ plug.VersionWeighter = (*sqlEngine)(nil)
	_ plug.Trasher         = (*sqlEngine)(nil)
	_ plug.DocumentSource  = (*sqlEngine)(nil)
	_ plug.Documents       = (*sqlEngine)(nil)
	_ plug.DocumentSink    = (*sqlEngine)(nil)

	_ plug.Database         = (*database)(nil)
	_ plug.Checker          = (*database)(nil)
	_ plug.MigratorProvider = (*database)(nil)
	_ plug.Migrator         = migrator{}
)
