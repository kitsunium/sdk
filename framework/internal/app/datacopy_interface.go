package app

import (
	"context"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
)

// The ports a data copy reads and writes a table through (ADR 0178 C7):
// a store's documents as they rest on either side, and the nodes whose
// tables a copy fills.

// copyDocs is a store's documents as they rest: the store subsystem's
// engines', a workflow's journal file.
type copyDocs = plug.Documents

// copySink is a store's documents on the database a copy writes.
type copySink interface {
	plug.Documents
	plug.DocumentSink
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
