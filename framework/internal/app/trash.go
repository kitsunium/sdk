package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// The trash (kitsunium/sdk ADR 0178 C4). A store with [Revisions] keeps what
// the product deletes for <store>-trash — 30 days unless the setting says
// otherwise, 0 turning it off —, as WordPress keeps a deleted post: gone
// from every read, its unique keys free, its versions kept, restorable with
// [StoreService.Untrash]. Kit empties it: once at start, for what is past
// due, then at each record's own instant on the app's clock (trash_run.go)
// — an agenda, never a sweep. The SDK's document store keeps the trash, in
// both engines (ADR 0143, amended for ADR 0178): a trashed document is a
// former version without a current one.
//
// A deletion kit makes for privacy never goes to the trash: a retention's
// deletion, a deletion on request (Records.Delete, app.DeleteOnErasure) and
// an erasure delete at once, as before; a person's erasure purges their
// records from the trash too, and journals each; a trashed record past its
// retention's instant is purged then, before its 30 days.

// trashSuffix names a store's trash setting: <store>-trash.
const trashSuffix = "trash"

// trashDefault is how long a store keeps what the product deletes, unless
// its setting says otherwise: WordPress's EMPTY_TRASH_DAYS.
const trashDefault time.Duration = 30 * 24 * time.Hour

// untrashLabel labels Untrash's edge: a write the diagram tells apart.
const untrashLabel = "untrash"

// TombstoneEvent is one record in a store's trash: its key, when the product
// deleted it, on the app's clock, and when the framework purges it — its trash's
// length after the deletion, or sooner when its retention says so.
type TombstoneEvent struct {
	Key     string
	At      time.Time
	PurgeAt time.Time
}

// declareTrash declares the store's trash setting, <store>-trash: a store
// with revisions keeps what the product deletes that long. The name is the
// store's in a setting's grammar — lower case, dashes for its other marks.
func (s *storeCore) declareTrash(svc *Service) {
	name := trashSettingName(s.name)
	if secret.ValidateName(name) != nil {
		svc.problem(s.decl, s.id, "store.trash-name", "store", s.name, "setting", name, "max", secret.MaxNameLen)
		return
	}
	st := NewSettingService(trashDefault)
	st.svc, st.name, st.decl = svc, name, s.decl
	st.id = svc.name + "/setting/" + name
	st.check = func(v any) phrase {
		if d, ok := v.(time.Duration); ok && d < 0 {
			return say("store.trash-negative")
		}
		return phrase{}
	}
	svc.mu.Lock()
	svc.settings = append(svc.settings, st)
	svc.mu.Unlock()
	s.trashFor = st
}

// trashSettingName is <store>-trash, the store's name in a setting's
// grammar: lower case, an underscore or a dot written as a dash.
func trashSettingName(store string) string {
	flat := strings.Map(func(r rune) rune {
		switch {
		case r == '_' || r == '.':
			return '-'
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return r
	}, store)
	return flat + "-" + trashSuffix
}

// trashLength is how long the store keeps what the product deletes, in the
// app that runs it: none for a store without revisions.
func (s *storeCore) trashLength() time.Duration {
	if s.revisions == 0 || s.trashFor == nil {
		return 0
	}
	return max(s.trashFor.Get(), 0)
}

// trashes reports whether a deletion by the product moves the record to the
// store's trash.
func (s *storeCore) trashes() bool {
	if s.trashLength() == 0 {
		return false
	}
	_, ok := s.engine().(plug.Trasher)
	return ok
}

// trashKeeper is the engine that keeps the store's trash: an [Invalid] for
// a store without revisions, which keeps none.
func (s *storeCore) trashKeeper() (plug.Trasher, error) {
	eng := s.engine()
	if eng == nil {
		return nil, notRunning(&s.nodeBase)
	}
	tr, ok := eng.(plug.Trasher)
	if s.revisions == 0 || !ok {
		return nil, Invalid(fmt.Sprintf("%s keeps no trash: declare it with app.Revisions(n)", s.name))
	}
	return tr, nil
}

// Untrash brings the record the product deleted under key back from the
// store's trash, as it was when deleted — its versions kept, no version
// made — and returns it. A key with no record in the trash — never deleted,
// purged, or written again since — is a [NotFound]; a unique index another
// record took a key of meanwhile refuses it with a [Conflict], and it stays
// in the trash; a store without [Revisions] keeps no trash: an [Invalid].
func (s *StoreService[T]) Untrash(ctx context.Context, key string) (T, error) {
	a := s.app()
	if a == nil {
		var zero T
		return zero, notRunning(&s.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: model.EdgeWrites, label: untrashLabel, op: model.OpWrite, name: "Untrash"})
	v, err := s.untrash(ctx, key)
	sp.end(err)
	return as[T](v), err
}

// Trashed returns the records in the store's trash, in key order: each
// one's key, when the product deleted it, and when the framework purges it. A store
// without [Revisions] keeps no trash: an [Invalid].
func (s *StoreService[T]) Trashed(ctx context.Context) ([]TombstoneEvent, error) {
	a := s.app()
	if a == nil {
		return nil, notRunning(&s.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: model.EdgeReads, op: model.OpRead, name: "Trashed"})
	out, err := s.trashed(ctx, a)
	sp.end(err)
	return out, err
}

// untrash brings the record under key back from the trash: a funnel, which
// tells the store's watches of the write once kit confirms it.
func (s *storeCore) untrash(ctx context.Context, key string) (any, error) {
	tr, err := s.trashKeeper()
	if err != nil {
		return nil, err
	}
	ctx, release, err := s.turn(ctx)
	defer release()
	if err != nil {
		return nil, err
	}
	ctx = s.outside(ctx)
	if err := s.trashSaid(tr.Untrash(ctx, key), key); err != nil {
		return nil, err
	}
	// The agenda keeps its entry: a rollback puts the record back in the
	// trash, and a record no longer there passes its instant doing nothing.
	s.notify(ctx, key, false)
	return s.read(ctx, key)
}

// trashed lists the trash, each record with the instant kit purges it.
func (s *storeCore) trashed(ctx context.Context, a *App) ([]TombstoneEvent, error) {
	tr, err := s.trashKeeper()
	if err != nil {
		return nil, err
	}
	stones, err := tr.Tombstones(s.outside(ctx))
	if err != nil {
		return nil, s.trashSaid(err, "")
	}
	out := make([]TombstoneEvent, 0, len(stones))
	for _, st := range stones {
		out = append(out, TombstoneEvent{Key: st.Key, At: st.At, PurgeAt: s.purgeAt(ctx, a, st)})
	}
	return out, nil
}

// trashRecord moves the record under key to the trash: Store.Delete's,
// on a store that keeps one. A funnel: the store's watches hear of a
// deletion, and the trash's agenda of the record.
func (s *storeCore) trashRecord(ctx context.Context, tr plug.Trasher, key string) error {
	ctx, release, err := s.turn(ctx)
	defer release()
	if err != nil {
		return err
	}
	ctx = s.outside(ctx)
	if err := s.said(tr.Trash(ctx, key), key, ""); err != nil {
		return err
	}
	s.notify(ctx, key, true)
	if a := s.app(); a != nil {
		s.trashPlanned(ctx, a, plug.TombstoneValue{Key: key, At: a.clock.Now()})
	}
	return nil
}

// discard is how Store.Delete removes the record under key: to the trash on
// a store that keeps one, at once otherwise.
func (s *storeCore) discard(ctx context.Context, key string) error {
	if s.trashes() {
		if tr, ok := s.engine().(plug.Trasher); ok {
			return s.trashRecord(ctx, tr, key)
		}
	}
	return s.remove(ctx, key)
}

// purgeTrashed empties the trash of the record under key, under the data's
// writer turn — when scheduled is set, only a tombstone trashed no later
// than scheduled, the one the agenda named —: it reports false, with no
// error, when no such record is trashed there any more. The record's
// person's data key goes once nothing kit keeps needs it (shredIfLast).
func (s *storeCore) purgeTrashed(ctx context.Context, a *App, key string, scheduled time.Time) (bool, error) {
	tr, err := s.trashKeeper()
	if err != nil {
		return false, err
	}
	ctx, release, err := s.turn(ctx)
	defer release()
	if err != nil {
		return false, err
	}
	ctx = s.outside(ctx)
	subject, serr := s.trashedSubject(ctx, a, key)
	if serr != nil {
		// The purge goes on: the record leaves the trash whoever it was
		// about; only its person's key waits for their next deletion.
		logger.Warn(ctx, a.log, "the person of a record leaving the trash could not be read",
			logger.String("store", s.id), logger.String("error", describeText(serr)))
	}
	err = s.purgeScheduled(ctx, a, tr, key, scheduled)
	switch {
	case plug.Refused(err, plug.RefusalNotTrashed):
		return false, nil
	case err != nil && !plug.Refused(err, plug.RefusalUnconfirmed):
		return false, s.trashFailure(err)
	}
	if subject != "" {
		if err := a.shredIfLast(ctx, subject); err != nil {
			logger.Warn(ctx, a.log, "the data key of a person whose last record left the trash could not be destroyed",
				logger.String("store", s.id), logger.String("error", describeText(err)))
		}
	}
	return true, nil
}

// purgeScheduled purges the record trashed under key — when scheduled is
// set, only if its tombstone was trashed no later than scheduled, read and
// purged in one transaction on a database, under the writer turn elsewhere:
// a restoration and a new deletion since make a tombstone the agenda did not
// name, which waits for its own instant. DocumentNotTrashed when none is.
func (s *storeCore) purgeScheduled(ctx context.Context, a *App, tr plug.Trasher, key string, scheduled time.Time) error {
	purge := func(ctx context.Context) error {
		if !scheduled.IsZero() {
			stones, err := tr.Tombstones(ctx)
			if err != nil {
				return err
			}
			i := slices.IndexFunc(stones, func(st plug.TombstoneValue) bool { return st.Key == key })
			if i < 0 || stones[i].At.After(scheduled) {
				return frameworkRefusal(plug.RefusalNotTrashed, s.id)
			}
		}
		return tr.Purge(ctx, key)
	}
	if scheduled.IsZero() || s.local() || unitOf(ctx) != nil {
		return purge(ctx)
	}
	return transact(ctx, a, purge)
}

// eraseTrashed purges from the trash every record whose subject is one of
// ids, for their erasure, and journals each as a deletion: a person's
// erasure leaves nothing of them in the trash. It returns the keys purged.
// A record no data key opens any more is not theirs to judge: its subject
// cannot be read, and it is left for its purge — what sealed it is gone.
func (s *storeCore) eraseTrashed(ctx context.Context, a *App, ids []string, reason string) ([]string, error) {
	tr, ok := s.engine().(plug.Trasher)
	if s.revisions == 0 || !ok || s.plan().subject == nil {
		return nil, nil
	}
	stones, err := tr.Tombstones(s.outside(ctx))
	if err != nil {
		return nil, s.trashFailure(err)
	}
	var purged []string
	var failed []error
	for _, st := range stones {
		done, err := s.eraseOneTrashed(ctx, a, st.Key, ids, reason)
		if done {
			purged = append(purged, st.Key)
		}
		failed = append(failed, err)
	}
	return purged, errors.Join(failed...)
}

// eraseOneTrashed purges the record trashed under key when its subject is
// one of ids, and journals it: it reports whether it purged, even when the
// journal then failed.
func (s *storeCore) eraseOneTrashed(ctx context.Context, a *App, key string, ids []string, reason string) (bool, error) {
	subject, err := s.trashedSubject(ctx, a, key)
	if err != nil || subject == "" || !slices.Contains(ids, subject) {
		return false, err
	}
	done, err := s.purgeTrashed(ctx, a, key, time.Time{})
	if err != nil || !done {
		return false, err
	}
	s.counted(0, 1)
	return true, a.journal(ctx, &journalLine{op: model.JournalDelete, store: s.id, key: key, subject: subject, reason: reason})
}

// trashedValue is the record trashed under key, as it was deleted: opened,
// for a store that seals.
func (s *storeCore) trashedValue(ctx context.Context, key string) (any, error) {
	all, err := s.restingVersions(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, s.missing(key)
	}
	raw := all[0].JSON
	if se := s.sealing(); se != nil {
		if raw, err = se.openDoc(ctx, key, raw); err != nil {
			return nil, err
		}
	}
	v, err := s.decodeEntity(raw)
	if err != nil {
		return nil, s.decodeFailure(key, err)
	}
	return v, nil
}

// trashedSubject is the subject of the record trashed under key; "" when
// the store has none, kit keeps no data keys, or the record no longer opens:
// sealed under a key destroyed since, it says nothing of anyone.
func (s *storeCore) trashedSubject(ctx context.Context, a *App, key string) (string, error) {
	if s.plan().subject == nil {
		return "", nil
	}
	v, err := s.trashedValue(ctx, key)
	switch {
	case err == nil:
		return s.subjectOf(v), nil
	case isNotFound(err), errs.HasCode(err, CodeSealOpen), errs.HasCode(err, CodeSealKey),
		errs.HasCode(err, CodeStoreDecode), !a.keepsDataKeys():
		return "", nil
	}
	return "", err
}

// purgeAt is when kit purges a record of the trash: its trash's length
// after its deletion, or its retention's instant when that comes first.
func (s *storeCore) purgeAt(ctx context.Context, a *App, st plug.TombstoneValue) time.Time {
	at := st.At.Add(s.trashLength())
	if !s.retains() {
		return at
	}
	v, err := s.trashedValue(ctx, st.Key)
	if err != nil {
		return at
	}
	if due := s.firstDue(a, v); !due.IsZero() && due.Before(at) {
		return due
	}
	return at
}

// trashSaid is a refusal of the trash in kit's words: nothing in the trash
// under key is a [NotFound], a store without one an [Invalid].
func (s *storeCore) trashSaid(err error, key string) error {
	switch {
	case err == nil:
		return nil
	case plug.Refused(err, plug.RefusalNotTrashed):
		return NotFound(fmt.Sprintf("%s: no record %q in the trash", s.name, clip(key)))
	case plug.Refused(err, plug.RefusalVersionsNotKept):
		return Invalid(fmt.Sprintf("%s keeps no trash: declare it with app.Revisions(n)", s.name))
	}
	return s.said(err, key, "")
}

// trashFailure is the trash's own failure: it could not be read or emptied.
func (s *storeCore) trashFailure(err error) error {
	// Already kit's word — a refusal said — it is returned as said.
	if ke, ok := errors.AsType[*Error](err); ok && ke != nil {
		return err
	}
	return failure(CodeTrash, "TRASH", "a store's trash cannot be read or emptied", err, errs.String("store", s.id))
}
