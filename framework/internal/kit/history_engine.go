package kit

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The writes of a store that remembers (history.go): every one goes through
// the engine's Update, which sees the value it replaces; the history is
// written inside it, before the record, and put back when the engine then
// refuses the record.

// writeAttempts bounds how often a Put whose key is new goes back and forth
// between an insertion and an update while other writers insert and delete
// the same key.
const writeAttempts int = 3

// inOne runs a write of a store on a database in one transaction — the
// caller's, or one of its own —, so that the record and its history commit
// together, and the history's statements run in the record's transaction
// rather than wait for its locks on the pool. On the data directory and in
// memory the write runs as it is.
func (h *historied) inOne(ctx context.Context, write func(context.Context) error) error {
	if h.hist == nil || h.onDatabase() == nil || unitOf(ctx) != nil {
		return write(ctx)
	}
	return transact(ctx, h.s.app(), write)
}

// Write stores d as mode says, in one transaction on a database.
func (h *historied) Write(ctx context.Context, d plug.DocumentValue, mode plug.WriteMode) error {
	return h.inOne(ctx, func(ctx context.Context) error { return h.write(ctx, d, mode) })
}

// write stores d as mode says. A write that may replace a record goes
// through Update, which sees the value it replaces — the field's former
// value, the hash a policy's check compares: a Put whose key is new
// inserts, and becomes an update again when another writer inserted it
// meanwhile. A record the type no longer decodes is overwritten as before,
// remembering nothing of it.
func (h *historied) write(ctx context.Context, d plug.DocumentValue, mode plug.WriteMode) error {
	key := h.s.keyOf(d.Value)
	if key == "" {
		return h.StoreEngine.Write(ctx, d, mode) // the engine says why
	}
	if mode == insertOnly {
		return h.insert(ctx, key, d)
	}
	var err error
	for range writeAttempts {
		_, err = h.update(ctx, key, func(cur *plug.DocumentValue) error { *cur = d; return nil })
		switch {
		case errors.Is(err, docstore.DocumentUndecodable):
			return h.overwrite(ctx, d, mode)
		case mode == replaceOnly || !errors.Is(err, docstore.DocumentNotFound):
			return err
		}
		if err = h.insert(ctx, key, d); !errors.Is(err, docstore.DocumentExists) {
			return err
		}
	}
	return err
}

// insert stores a new record, which starts with no history: one left under
// its key by a record whose deletion failed half-way is removed first.
func (h *historied) insert(ctx context.Context, key string, d plug.DocumentValue) error {
	if err := h.s.checkPasswords(nil, d.Value); err != nil {
		return err
	}
	if h.hist != nil {
		if _, err := h.hist.Get(ctx, key); err == nil && !h.recordExists(ctx, key) {
			if err := h.remove(ctx, key); err != nil && !errors.Is(err, docstore.WriteUnconfirmed) {
				return err
			}
		}
	}
	return h.StoreEngine.Write(ctx, d, insertOnly)
}

// overwrite stores d over a record the type no longer decodes: its former
// values cannot be read, so the write records none.
func (h *historied) overwrite(ctx context.Context, d plug.DocumentValue, mode plug.WriteMode) error {
	if err := h.s.checkPasswords(nil, d.Value); err != nil {
		return err
	}
	return h.StoreEngine.Write(ctx, d, mode)
}

// Update applies fn to the record under key and stores the result, in one
// transaction on a database.
func (h *historied) Update(ctx context.Context, key string, fn func(d *plug.DocumentValue) error) (plug.DocumentValue, error) {
	var d plug.DocumentValue
	err := h.inOne(ctx, func(ctx context.Context) error {
		var err error
		d, err = h.update(ctx, key, fn)
		return err
	})
	return d, err
}

// update applies fn to the record under key and stores the result, its
// history first: the former values of the fields fn changed, written inside
// the engine's update. When the engine then refuses the record, the history
// is put back — on a database, the transaction undoes it.
func (h *historied) update(ctx context.Context, key string, fn func(d *plug.DocumentValue) error) (plug.DocumentValue, error) {
	var w historyWrite
	d, err := h.StoreEngine.Update(ctx, key, func(d *plug.DocumentValue) error {
		return h.updating(ctx, key, d, fn, &w)
	})
	switch {
	case err == nil && w.unconfirmed != nil:
		return d, w.unconfirmed
	case err != nil && !errors.Is(err, docstore.WriteUnconfirmed):
		h.undo(ctx, key, &w)
	}
	return d, err
}

// Delete removes the record under key, then its history, in one
// transaction on a database: in files, a crash between the two leaves a
// history whose record is gone, which the next start removes.
func (h *historied) Delete(ctx context.Context, key string) error {
	return h.inOne(ctx, func(ctx context.Context) error { return h.delete(ctx, key) })
}

// delete removes the record under key, then its history.
func (h *historied) delete(ctx context.Context, key string) error {
	err := h.StoreEngine.Delete(ctx, key)
	if h.hist == nil || (err != nil && !errors.Is(err, docstore.WriteUnconfirmed)) {
		return err
	}
	if rerr := h.remove(ctx, key); rerr != nil {
		return rerr
	}
	return err
}

// remove removes the history under key; none is no error.
func (h *historied) remove(ctx context.Context, key string) error {
	err := h.hist.Delete(ctx, key)
	switch {
	case err == nil, errors.Is(err, docstore.DocumentNotFound):
		return nil
	case errors.Is(err, docstore.WriteUnconfirmed):
		return err
	}
	return h.failed(CodeHistoryWrite, err)
}

// errHistoryMoved ends an undo: another write changed the history since.
var errHistoryMoved = errs.New(CodeHistoryMoved, "HISTORY_MOVED", "the history changed since", "kit: an undo found the history changed by another write")

// undo puts a record's history back when the engine refused the record
// after its history was written — unless a write since changed it, which
// already read past what the refused one left. A history the refused write
// made is put back empty rather than removed, which only a check outside
// the history's lock could do: an empty history reads as none, and the next
// start removes it.
func (h *historied) undo(ctx context.Context, key string, w *historyWrite) {
	if !w.stored {
		return
	}
	var err error
	if len(w.after.Fields) == 0 {
		// The refused write removed the history: it comes back, unless
		// another write made a new one meanwhile.
		var d plug.DocumentValue
		if d, err = historyDoc(w.before); err == nil {
			err = h.hist.Write(ctx, d, insertOnly)
		}
	} else {
		_, err = h.hist.Update(ctx, key, func(d *plug.DocumentValue) error {
			cur, err := historyOf(*d)
			if err != nil {
				return err
			}
			if !sameHistory(cur, w.after) {
				return errHistoryMoved
			}
			next, err := historyDoc(historyRecord{Key: key, Fields: w.before.Fields})
			*d = next
			return err
		})
	}
	switch {
	case err == nil, errors.Is(err, errHistoryMoved), errors.Is(err, docstore.DocumentExists),
		errors.Is(err, docstore.DocumentNotFound), errors.Is(err, docstore.WriteUnconfirmed):
	default:
		h.warn(ctx, err)
	}
}

// sameHistory reports whether two histories keep the same former values.
func sameHistory(x, y historyRecord) bool {
	return maps.EqualFunc(x.Fields, y.Fields, func(a, b []formerEntry) bool {
		return slices.EqualFunc(a, b, func(e, f formerEntry) bool {
			return bytes.Equal(e.Value, f.Value) && e.Until.Equal(f.Until) && e.By == f.By
		})
	})
}
