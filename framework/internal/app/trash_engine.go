package app

import (
	"context"
	"errors"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
)

// The trash as the store's engines keep it (trash.go, ADR 0178 C4): the
// store subsystem's document store opened with versions, in memory, in files
// or over SQL, and the engines that wrap one, which hand the calls on — the
// siblings of plug.StoreEngine that plug.Trasher names.

// trashOf is the trash eng keeps, false for an engine that keeps none.
func trashOf(eng plug.StoreEngine) (plug.Trasher, bool) {
	tr, ok := eng.(plug.Trasher)
	return tr, ok
}

// Trash moves the record key to the trash; its former values stay, for a
// restoration.
func (h *historied) Trash(ctx context.Context, key string) error {
	tr, ok := trashOf(h.StoreEngine)
	if !ok {
		return errVersionsNotKept
	}
	return tr.Trash(ctx, key)
}

// Untrash brings the record key back, its former values with it.
func (h *historied) Untrash(ctx context.Context, key string) error {
	tr, ok := trashOf(h.StoreEngine)
	if !ok {
		return errVersionsNotKept
	}
	return tr.Untrash(ctx, key)
}

// Purge empties the trash of the record key, then removes its former
// values, in one transaction on a database.
func (h *historied) Purge(ctx context.Context, key string) error {
	tr, ok := trashOf(h.StoreEngine)
	if !ok {
		return errVersionsNotKept
	}
	return h.inOne(ctx, func(ctx context.Context) error {
		err := tr.Purge(ctx, key)
		if h.hist == nil || (err != nil && !plug.Refused(err, plug.RefusalUnconfirmed)) {
			return err
		}
		return errors.Join(err, h.remove(ctx, key))
	})
}

// Tombstones are the records in the trash.
func (h *historied) Tombstones(ctx context.Context) ([]plug.TombstoneValue, error) {
	tr, ok := trashOf(h.StoreEngine)
	if !ok {
		return nil, errVersionsNotKept
	}
	return tr.Tombstones(ctx)
}

// Trash moves the record key to the trash, sealed as it rests: its own data
// key stays, for a restoration.
func (e *sealedEngine) Trash(ctx context.Context, key string) error {
	tr, ok := trashOf(e.inner)
	if !ok {
		return errVersionsNotKept
	}
	return tr.Trash(ctx, key)
}

// Untrash brings the record key back, sealed as it rested.
func (e *sealedEngine) Untrash(ctx context.Context, key string) error {
	tr, ok := trashOf(e.inner)
	if !ok {
		return errVersionsNotKept
	}
	return tr.Untrash(ctx, key)
}

// Purge empties the trash of the record key, then destroys its own data
// key, when it had one — at the commit, in a transaction —, as Delete does.
func (e *sealedEngine) Purge(ctx context.Context, key string) error {
	tr, ok := trashOf(e.inner)
	if !ok {
		return errVersionsNotKept
	}
	err := tr.Purge(ctx, key)
	if (err != nil && !plug.Refused(err, plug.RefusalUnconfirmed)) || !e.s.plan().sealsAny() {
		return err
	}
	e.dropOwnKeyAfter(ctx, key)
	return err
}

// Tombstones are the records in the trash.
func (e *sealedEngine) Tombstones(ctx context.Context) ([]plug.TombstoneValue, error) {
	tr, ok := trashOf(e.inner)
	if !ok {
		return nil, errVersionsNotKept
	}
	return tr.Tombstones(ctx)
}
