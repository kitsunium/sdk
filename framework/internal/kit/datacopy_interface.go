package kit

import (
	"context"
	"encoding/json"
	"iter"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
)

// The ports a data copy reads and writes a table through (ADR 0178 C7):
// a store's documents as they rest on either side, and the nodes whose
// tables a copy fills.

// copyDocs is a store's documents as they rest.
type copyDocs interface {
	// keyList yields every key the store holds, in key order.
	keyList(ctx context.Context) iter.Seq2[string, error]
	// tombstoneList is the trash, in key order: none when the store keeps
	// no versions.
	tombstoneList(ctx context.Context) ([]docstore.Tombstone, error)
	// versionList is the versions of the document key, newest first.
	versionList(ctx context.Context, key string) ([]docstore.Version, error)
	// current is the document key as the store keeps it.
	current(ctx context.Context, key string) (json.RawMessage, error)
	// keepsVersions reports whether the store keeps versions.
	keepsVersions() bool
}

// copySink is a store's documents on the database a copy writes.
type copySink interface {
	copyDocs
	// putDocument writes the document key with its versions and its trash
	// as they are given.
	putDocument(ctx context.Context, key string, versions []docstore.Version, trashedAt time.Time) error
	// insertRaw writes a document of a store that keeps no versions, as it
	// rests.
	insertRaw(ctx context.Context, raw json.RawMessage) error
}

// documenter is an engine that says the document store it runs on: the
// document store itself, or what the history and the sealing put in front
// of it, through to it.
type documenter interface {
	// document is the document store the engine runs on, and whether there
	// is one.
	document() (copyDocs, bool)
}

// copyPartser is a store a data copy moves, whatever its entity type.
type copyPartser interface {
	node
	placementSource
	// copyParts are the store's tables on r, each read from the data
	// directory and written on r.
	copyParts(ctx context.Context, a *App, r *databaseRun) ([]copyPart, error)
}

// journalParter is a workflow whose journal a data copy moves.
type journalParter interface {
	journaled
	// journalPart is the workflow's journal, read from its file and
	// written in its table on r.
	journalPart(a *App, r *databaseRun) (*copyPart, error)
}
