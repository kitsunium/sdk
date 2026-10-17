package kit

import (
	"bytes"
	"context"
	"encoding/json"
	"iter"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
)

// The document stores a data copy reads and writes through (ADR 0178 C7):
// the file engine under the data directory, the SQL engine on the database,
// each reached through what the history and the sealing put in front of it.

// documentsOf is the document store eng runs on, through what wraps it.
func documentsOf(eng plug.StoreEngine) (copyDocs, bool) {
	d, ok := eng.(documenter)
	if !ok {
		return nil, false
	}
	return d.document()
}

// document is the store's engine behind the history.
func (h *historied) document() (copyDocs, bool) { return documentsOf(h.StoreEngine) }

// document is the store's document store behind the sealing.
func (e *sealedEngine) document() (copyDocs, bool) { return documentsOf(e.inner) }

// document is the file store itself.
func (e *docEngine) document() (copyDocs, bool) { return e, true }

// document is the table itself.
func (e *sqlEngine) document() (copyDocs, bool) { return e, true }

// keyList yields the keys of the file store.
//
//ktn:allow-unused-param: the copyDocs interface passes a context the local document store does not take
func (e *docEngine) keyList(_ context.Context) iter.Seq2[string, error] { return e.ds.Keys("") }

// tombstoneList is the file store's trash.
//
//ktn:allow-unused-param: the copyDocs interface passes a context the local document store does not take
func (e *docEngine) tombstoneList(_ context.Context) ([]docstore.Tombstone, error) {
	if e.keep == 0 {
		return nil, nil
	}
	return e.ds.Tombstones()
}

// versionList is the versions of the document key in the file store.
//
//ktn:allow-unused-param: the copyDocs interface passes a context the local document store does not take
func (e *docEngine) versionList(_ context.Context, key string) ([]docstore.Version, error) {
	return e.ds.Versions(key)
}

// current is the document key as the file store keeps it.
//
//ktn:allow-unused-param: the copyDocs interface passes a context the local document store does not take
func (e *docEngine) current(_ context.Context, key string) (json.RawMessage, error) {
	r, err := e.ds.Get(key)
	if err != nil {
		return nil, err
	}
	return r.raw, nil
}

// keepsVersions reports whether the file store keeps versions.
func (e *docEngine) keepsVersions() bool { return e.keep > 0 }

// keyList yields the keys of the table, a page at a time.
func (e *sqlEngine) keyList(ctx context.Context) iter.Seq2[string, error] {
	return e.ds.Keys(ctx, "")
}

// tombstoneList is the table's trash.
func (e *sqlEngine) tombstoneList(ctx context.Context) ([]docstore.Tombstone, error) {
	if e.keep == 0 {
		return nil, nil
	}
	return e.ds.Tombstones(ctx)
}

// versionList is the versions of the document key in the table.
func (e *sqlEngine) versionList(ctx context.Context, key string) ([]docstore.Version, error) {
	return e.ds.Versions(ctx, key)
}

// current is the document key as the table keeps it.
func (e *sqlEngine) current(ctx context.Context, key string) (json.RawMessage, error) {
	r, err := e.ds.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return r.raw, nil
}

// keepsVersions reports whether the table keeps versions.
func (e *sqlEngine) keepsVersions() bool { return e.keep > 0 }

// putDocument writes the document key into the table with its versions and
// its trash as they are given.
func (e *sqlEngine) putDocument(ctx context.Context, key string, versions []docstore.Version, trashedAt time.Time) error {
	return e.ds.PutDocument(ctx, key, versions, trashedAt)
}

// insertRaw writes a document, as it rests, into a table that keeps no
// versions: the record as it rests — sealed members boxes for a store that
// seals —, keyed and indexed as the table's engine keys and indexes it.
func (e *sqlEngine) insertRaw(ctx context.Context, raw json.RawMessage) error {
	return e.ds.Insert(ctx, docRecord{raw: bytes.Clone(raw), open: &openedDoc{}})
}
