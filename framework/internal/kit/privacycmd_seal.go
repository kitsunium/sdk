// Package kit — `privacy seal`: a whole store sealed now.
package kit

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
)

// The parts of a record that may keep a member in clear.
const (
	clearRecord clearParts = 1 << iota
	clearFormer
	clearVersions
)

// clearParts are the parts of a record that keep in clear a member kit
// seals: the record itself, its former values, its versions.
type clearParts uint8

// `privacy seal STORE…` (ADR 0006 §4): a member stored before its field was
// sealed is read as written and sealed at its record's next write; the
// command seals a whole store now — every record that keeps such a member in
// clear, and every former value kept in clear —, as their next write would,
// and changes no value.

// sealAller is a store the privacy command can seal, whatever its
// entity type.
type sealAller interface {
	node
	sealingSource
	sealAll(ctx context.Context) (sealed, total int, err error)
}

// sealCommand runs `privacy seal STORE…`.
func (a *App) sealCommand(ctx context.Context, stores []string, stdout, stderr io.Writer) int {
	if len(stores) == 0 {
		fmt.Fprintf(stderr, "usage: %s privacy seal STORE…   a store's node ID, as privacy register names it\n", a.name)
		return 2
	}
	return a.onData(ctx, stderr, func(ctx context.Context) error {
		var failed []error
		for _, id := range stores {
			failed = append(failed, a.sealStore(ctx, id, stdout))
		}
		return errors.Join(failed...)
	})
}

// sealStore seals the store whose node ID is id, and says what it did.
func (a *App) sealStore(ctx context.Context, id string, stdout io.Writer) error {
	st, ok := a.findNode(id).(sealAller)
	if !ok || model.ServiceOf(id) == privacyService && id != privacyService+"/store/holds" && id != privacyService+"/store/journal" {
		return NotFound("no such store: " + clip(id))
	}
	if !st.sealsAtRest(a) {
		return Invalid(clip(id) + " seals nothing at rest: its entity has no personal, special or secret field that is not plain, or it is kept in memory")
	}
	sealed, total, err := st.sealAll(ctx)
	fmt.Fprintf(stdout, "%s: %d of %d records sealed now, the others were already\n", id, sealed, total)
	return err
}

// sealAll seals every record of the store that keeps a member it seals in
// clear, and every former value and version kept in clear, then folds the
// store: what was in clear leaves its files. It says how many records it sealed, of how
// many.
func (s *StoreService[T]) sealAll(ctx context.Context) (sealed, total int, err error) {
	a, se := s.app(), s.sealing()
	if a == nil || se == nil {
		return 0, 0, nil
	}
	ctx, sp := a.beginPrivacy(ctx, s.id, model.EdgeWrites, "seal", "Seal")
	defer func() { sp.end(err) }()
	entries, err := se.resting(ctx, 0)
	if err != nil {
		return 0, 0, s.said(err, "", "")
	}
	for _, en := range entries {
		total++
		done, err := s.sealOne(ctx, se, en.Key)
		if err != nil {
			return sealed, total, err
		}
		if done {
			sealed++
		}
	}
	if sealed > 0 {
		err = s.fold(ctx)
	}
	return sealed, total, err
}

// sealOne seals the record under key and its former values, when it keeps
// one in clear, and reports whether it did.
func (s *StoreService[T]) sealOne(ctx context.Context, se *sealedEngine[T], key string) (bool, error) {
	record, err := se.inClear(ctx, key)
	switch {
	case errors.Is(err, docstore.DocumentNotFound):
		return false, nil
	case err != nil:
		return false, s.said(err, key, "")
	}
	h := s.historied()
	former := h != nil && h.inClear(ctx, key)
	versions := s.versionsInClear(ctx, key)
	if !record && !former && !versions {
		return false, nil
	}
	return true, s.sealInClear(ctx, se, key, partsOf(record, former, versions))
}

// partsOf are the parts record, former and versions say keep a member in
// clear.
func partsOf(record, former, versions bool) clearParts {
	var p clearParts
	if record {
		p |= clearRecord
	}
	if former {
		p |= clearFormer
	}
	if versions {
		p |= clearVersions
	}
	return p
}

// sealInClear seals the parts of the record under key that keep a member
// in clear.
func (s *StoreService[T]) sealInClear(ctx context.Context, se *sealedEngine[T], key string, parts clearParts) error {
	if parts&clearRecord != 0 {
		// A write that changes nothing: the engine seals what it writes.
		if _, err := s.engine().Update(ctx, key, func(*T) error { return nil }); err != nil {
			return s.said(err, key, "")
		}
	}
	if parts&(clearFormer|clearVersions) == 0 {
		return nil
	}
	return s.sealKept(ctx, se, key, parts&clearFormer != 0, parts&clearVersions != 0)
}

// sealKept seals, under the record's data key, what the record under key
// keeps beside it in clear: its former values, its versions.
func (s *StoreService[T]) sealKept(ctx context.Context, se *sealedEngine[T], key string, former, versions bool) error {
	v, err := se.Get(ctx, key)
	if err != nil {
		return s.said(err, key, "")
	}
	ref, err := se.refOf(ctx, key, v)
	if err != nil {
		return err
	}
	if former {
		if err := s.historied().sealFormer(ctx, key, ref); err != nil {
			return err
		}
	}
	if versions {
		return s.sealVersions(ctx, key, ref)
	}
	return nil
}
