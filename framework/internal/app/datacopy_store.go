package app

import (
	"context"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A store's documents as a data copy reads and writes them (ADR 0178 C7):
// as they rest, through the SDK's document store on either side — the
// file engine under the data directory, the SQL engine on the database —,
// a sealed member a box neither side opens.

// copySample is how many records of a store that seals the copy reads back
// through kit, opened, before it commits: the keys were copied as boxes
// too, and only an opened record says they open.
const copySample int = 3

// copyPart is one table a data copy fills: its name, what is read from the
// data directory and what is written on the database, and — for a store
// that seals — a read of a few records through kit, opened.
type copyPart struct {
	from copyDocs
	to   copySink
	// sample reads up to copySample records of the table through kit,
	// opened; nil when the store seals nothing.
	sample func(ctx context.Context) error
	// name is the table's.
	name string
}

// copyParts are the store's tables on r — its own, its versions and trash
// within it, its records' former values — each read from the store as it
// started under the data directory, and written through the engine the
// start would open on r: its index keys hashed, its records sealed as they
// rest.
func (s *storeCore) copyParts(ctx context.Context, a *App, r *databaseRun) ([]copyPart, error) {
	from, ok := documentsOf(s.engine())
	if !ok {
		return nil, failure(CodeDataCopy, "DATA_COPY_STORE", "a store is not on the data directory: it cannot be copied", nil,
			errs.String("store", s.id))
	}
	opened, err := s.openCopyTarget(ctx, a, r)
	if err != nil {
		return nil, err
	}
	to, ok := documentsOf(opened)
	sink, sinks := to.(copySink)
	if !ok || !sinks {
		return nil, failure(CodeDataCopy, "DATA_COPY_STORE", "a store's table cannot be written by a copy", nil, errs.String("store", s.id))
	}
	part := copyPart{name: s.table(), from: from, to: sink}
	if _, seals := opened.(*sealedEngine); seals {
		part.sample = func(ctx context.Context) error { return sampleOpened(ctx, opened, sink) }
	}
	parts := []copyPart{part}
	hist, err := s.historyPart(r)
	if err != nil {
		return nil, err
	}
	if hist != nil {
		parts = append(parts, *hist)
	}
	return parts, nil
}

// openCopyTarget opens the store's engine on r as the start opens it there:
// kit's own index beside the product's, its index keys hashed under kit's
// index key, behind the sealing when the store seals.
//
// IFACE-OPAQUE: the engine is kit's own port — the SQL engine, or the
// sealing one around it — as openSQL returns it.
func (s *storeCore) openCopyTarget(ctx context.Context, a *App, r *databaseRun) (plug.StoreEngine, error) {
	own, err := s.frameworkIndexes(ctx, a)
	if err != nil {
		return nil, err
	}
	specs := append(s.specs(), own...)
	at := sqlTable{run: r, table: s.table(), store: s.id, own: frameworkOwn(s.svc), versions: s.versionsOn(a)}
	if len(specs) > 0 {
		keys, err := a.referenceKeys(ctx)
		if err != nil {
			return nil, err
		}
		at.indexKey, at.fingerprint = keys.indexKey, indexFingerprint(specsOf(specs), keys)
	}
	return s.openSQL(ctx, a, at, specs)
}

// historyPart is the store's records' former values, when it keeps any:
// read from its history under the data directory, written in its history's
// table on r.
func (s *storeCore) historyPart(r *databaseRun) (*copyPart, error) {
	h, ok := s.engine().(*historied)
	if !ok || h.hist == nil {
		return nil, nil
	}
	from, ok := documentsOf(h.hist)
	if !ok {
		return nil, nil
	}
	hist, err := s.historyOnSQL(r)
	if err != nil {
		return nil, err
	}
	to, ok := documentsOf(hist)
	sink, sinks := to.(copySink)
	if !ok || !sinks {
		return nil, failure(CodeDataCopy, "DATA_COPY_STORE", "a store's history cannot be written by a copy", nil, errs.String("store", s.id))
	}
	return &copyPart{name: s.historyTable(), from: from, to: sink}, nil
}

// sampleOpened reads the first records of the table through the store's
// engine, which opens them: a box whose key did not come across, or does
// not open, fails the copy.
func sampleOpened(ctx context.Context, eng plug.StoreEngine, keys copyDocs) error {
	read := 0
	for key, err := range keys.KeyList(ctx) {
		if err != nil {
			return err
		}
		if _, err := eng.Get(ctx, key); err != nil {
			return err
		}
		if read++; read == copySample {
			break
		}
	}
	return nil
}
