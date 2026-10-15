package kit

import (
	"context"
	"encoding/json"
	"iter"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
)

// The document stores a data copy reads and writes through (ADR 0178 C7):
// the file engine under the data directory, the SQL engine on the database,
// each reached through what the history and the sealing put in front of it.

// documentsOf is the document store eng runs on, through what wraps it.
func documentsOf[T any](eng storeEngine[T]) (copyDocs, bool) {
	d, ok := eng.(documenter)
	if !ok {
		return nil, false
	}
	return d.document()
}

// document is the store's engine behind the history.
func (h *historied[T]) document() (copyDocs, bool) { return documentsOf(h.storeEngine) }

// document is the store's document store behind the sealing.
func (e *sealedEngine[T]) document() (copyDocs, bool) { return documentsOf(e.inner) }

// document is the file store itself.
func (e *docEngine[T]) document() (copyDocs, bool) { return e, true }

// document is the table itself.
func (e *sqlEngine[T]) document() (copyDocs, bool) { return e, true }

// keyList yields the keys of the file store.
func (e *docEngine[T]) keyList(_ context.Context) iter.Seq2[string, error] { return e.ds.Keys("") }

// tombstoneList is the file store's trash.
func (e *docEngine[T]) tombstoneList(_ context.Context) ([]docstore.Tombstone, error) {
	if e.keep == 0 {
		return nil, nil
	}
	return e.ds.Tombstones()
}

// versionList is the versions of the document key in the file store.
func (e *docEngine[T]) versionList(_ context.Context, key string) ([]docstore.Version, error) {
	return e.ds.Versions(key)
}

// current is the document key as the file store keeps it.
func (e *docEngine[T]) current(_ context.Context, key string) (json.RawMessage, error) {
	v, err := e.ds.Get(key)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// keepsVersions reports whether the file store keeps versions.
func (e *docEngine[T]) keepsVersions() bool { return e.keep > 0 }

// keyList yields the keys of the table, a page at a time.
func (e *sqlEngine[T]) keyList(ctx context.Context) iter.Seq2[string, error] {
	return e.ds.Keys(ctx, "")
}

// tombstoneList is the table's trash.
func (e *sqlEngine[T]) tombstoneList(ctx context.Context) ([]docstore.Tombstone, error) {
	if e.keep == 0 {
		return nil, nil
	}
	return e.ds.Tombstones(ctx)
}

// versionList is the versions of the document key in the table.
func (e *sqlEngine[T]) versionList(ctx context.Context, key string) ([]docstore.Version, error) {
	return e.ds.Versions(ctx, key)
}

// current is the document key as the table keeps it.
func (e *sqlEngine[T]) current(ctx context.Context, key string) (json.RawMessage, error) {
	v, err := e.ds.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// keepsVersions reports whether the table keeps versions.
func (e *sqlEngine[T]) keepsVersions() bool { return e.keep > 0 }

// putDocument writes the document key into the table with its versions and
// its trash as they are given.
func (e *sqlEngine[T]) putDocument(ctx context.Context, key string, versions []docstore.Version, trashedAt time.Time) error {
	return e.ds.PutDocument(ctx, key, versions, trashedAt)
}

// insertRaw writes a document, as it rests, into a table that keeps no
// versions: decoded into what the table stores — a record as it rests for a
// store that seals, which encodes back to the same bytes — and inserted.
func (e *sqlEngine[T]) insertRaw(ctx context.Context, raw json.RawMessage) error {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	return e.ds.Insert(ctx, v)
}
