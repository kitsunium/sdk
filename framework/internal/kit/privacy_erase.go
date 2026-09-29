// Package kit — erasure: a record's personal data removed as its retention
// would.
package kit

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// What an erasure did with a record.
const (
	// outcomeNone found nothing to do.
	outcomeNone erasureOutcome = iota
	// outcomeErased erased the record's personal fields.
	outcomeErased
	// outcomeDeleted deleted the record.
	outcomeDeleted
	// outcomeHeld left the record, which a legal hold keeps.
	outcomeHeld
)

// errNothingToErase and errRekeyed end an erasure's update without writing.
var (
	errNothingToErase = errs.New(CodeNothingToErase, "NOTHING_TO_ERASE", "nothing to erase", "kit: an erasure's update found no personal value left")
	errRekeyed        = errs.New(CodeKeyRekeyed, "KEY_REKEYED", "the key depends on personal data", "kit: an erasure's update would change the record's key")
)

// erasureOutcome is what a person's erasure did with one record.
type erasureOutcome int

// Erasure is what kit.Erase did, store by store.
type Erasure = model.Erasure

// Erasure (ADR 0006 §5, §6): a record's personal, special and secret
// members and its subject cleared — after the store's Anonymise function has
// kept what it generalises —, its erased fields stamped, the rest kept; or
// the record deleted. What an erasure overwrote leaves the store's files
// when kit folds it, even when the journal then fails.

// Erase erases one record now, as its retention would: its personal,
// special and secret members and its subject cleared — after the store's
// Anonymise function has kept what it generalises —, its erased fields
// stamped, the rest kept. A held record is refused with a [Conflict] that
// names the hold, never a placed hold's reason. reason is journaled, for
// whoever runs the product.
func (s *StoreService[T]) Erase(ctx context.Context, key, reason string) error {
	a := s.app()
	if a == nil {
		return notRunning(&s.nodeBase)
	}
	ctx, sp := a.beginPrivacy(ctx, s.id, model.EdgeWrites, "", "Erase")
	unlock := a.lockHolds(s.id)
	err := s.holdRefusal(ctx, a, key)
	wrote := false
	if err == nil {
		wrote, err = s.eraseRecord(ctx, a, key, reason)
	}
	unlock()
	if wrote {
		err = errors.Join(err, s.fold(ctx))
	}
	sp.end(err)
	return err
}

// eraseRecord clears the record under key and journals it. It reports
// whether it wrote — even when the journal then failed, so that the caller
// folds what it overwrote; a record with nothing left to erase is left as
// it is. When clearing would change the record's key — a key built from
// personal data — the record is deleted instead, and the store says so
// once. The caller holds the store's hold lock, and checked the holds.
//
// Sealing (step 3) plugs in here: the record's boxes are opened when it is
// read, and nothing more is needed — clearing a member removes its box.
func (s *StoreService[T]) eraseRecord(ctx context.Context, a *App, key, reason string) (bool, error) {
	subject, err := s.clearRecord(ctx, key, a.clock.Now().UTC())
	switch {
	case errors.Is(err, errNothingToErase):
		return false, nil
	case errors.Is(err, errRekeyed):
		return s.eraseRekeyed(ctx, a, key, reason)
	case err != nil:
		return false, err
	}
	s.counted(1, 0)
	return true, a.journal(ctx, &journalLine{op: model.JournalErase, store: s.id, key: key, subject: subject, reason: reason})
}

// clearRecord runs the store's Anonymise function on the record under key,
// clears its sensitive members and stamps its erased fields, in one update,
// and returns the record's subject. The former values of what it clears go
// with them (ADR 0007), a record whose members are cleared already but
// whose history remembers them included. errNothingToErase and errRekeyed
// say it wrote nothing.
func (s *StoreService[T]) clearRecord(ctx context.Context, key string, now time.Time) (subject string, err error) {
	plan, panicked := s.plan(), false
	_, err = s.modify(s.erasing(ctx, nil), key, func(v *T) error {
		rv := reflect.ValueOf(v).Elem()
		if plan.cleared(rv) && !s.keepsErasable(ctx, key) {
			return errNothingToErase
		}
		subject, _ = plan.subjectOf(rv)
		if panicked = !s.anonymise(v); panicked {
			return errNothingToErase
		}
		plan.clearSensitive(rv)
		plan.stampErased(rv, now)
		if s.keyOf(*v) != key {
			return errRekeyed
		}
		return nil
	})
	if panicked {
		return "", failure(CodePrivacyErase, "ANONYMISE_PANICKED", "the store's Anonymise function panicked", nil, errs.String("store", s.id))
	}
	if endedWithoutWriting(err) {
		return subject, err
	}
	return "", err
}

// endedWithoutWriting reports whether an erasure's update ended as it
// should: done, or left without a write because nothing was left to erase or
// the record changed its key.
func endedWithoutWriting(err error) bool {
	return err == nil || errors.Is(err, errNothingToErase) || errors.Is(err, errRekeyed)
}

// anonymise runs the store's Anonymise function on v, when it declares one;
// false when the function panicked.
func (s *StoreService[T]) anonymise(v *T) (ok bool) {
	p := s.privacy
	if p == nil || p.anonymise == nil {
		return true
	}
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	p.anonymise(v)
	return true
}

// eraseRekeyed deletes a record whose erasure would change its key, and
// says so once for the store.
func (s *StoreService[T]) eraseRekeyed(ctx context.Context, a *App, key, reason string) (bool, error) {
	if p := s.privacy; p != nil {
		p.mu.Lock()
		first := !p.rekeyed
		p.rekeyed = true
		p.mu.Unlock()
		if first {
			a.problem(s.id, say("privacy.rekeyed", "store", s.id))
		}
	}
	v, err := s.read(ctx, key)
	if err != nil {
		return false, err
	}
	return s.deleteRecord(ctx, a, key, v, reason)
}

// deleteRecord removes the record under key, v as it was, and journals it.
// It reports whether it removed the record, even when the journal then
// failed. The caller holds the store's hold lock, and checked the holds.
func (s *StoreService[T]) deleteRecord(ctx context.Context, a *App, key string, v T, reason string) (bool, error) {
	if err := s.remove(ctx, key); err != nil {
		return false, err
	}
	s.counted(0, 1)
	return true, a.journal(ctx, &journalLine{op: model.JournalDelete, store: s.id, key: key, subject: s.subjectOf(v), reason: reason})
}

// counted adds to what this run erased and deleted, for the graph.
func (s *StoreService[T]) counted(erased, deleted int) {
	if p := s.privacy; p != nil {
		p.mu.Lock()
		p.erased += erased
		p.deleted += deleted
		p.mu.Unlock()
	}
}

// eraseOnRequest erases, or deletes where the store says DeleteOnErasure,
// one record of a person who asked; a held record is left and reported. The
// outcome says what changed, even when the journal then failed.
func (s *StoreService[T]) eraseOnRequest(ctx context.Context, a *App, key, reason string) (erasureOutcome, error) {
	unlock := a.lockHolds(s.id)
	defer unlock()
	switch err := s.holdRefusal(ctx, a, key); {
	case isConflict(err):
		return outcomeHeld, nil
	case err != nil:
		return outcomeNone, err
	}
	if s.privacy != nil && s.privacy.erasureDelete {
		return s.deleteOnRequest(ctx, a, key, reason)
	}
	wrote, err := s.eraseRecord(ctx, a, key, reason)
	if wrote {
		return outcomeErased, err
	}
	return outcomeNone, err
}

// deleteOnRequest deletes one record of a person who asked.
func (s *StoreService[T]) deleteOnRequest(ctx context.Context, a *App, key, reason string) (erasureOutcome, error) {
	v, err := s.read(ctx, key)
	switch {
	case isNotFound(err):
		return outcomeNone, nil
	case err != nil:
		return outcomeNone, err
	}
	removed, err := s.deleteRecord(ctx, a, key, v, reason)
	if removed {
		return outcomeDeleted, err
	}
	return outcomeNone, err
}

// fold folds what the store's writes left beside its data into it, so that
// what an erasure overwrote leaves the store's files: an engine that keeps
// such files folds them (storeFolder); a store in memory has nothing to
// fold.
func (s *StoreService[T]) fold(ctx context.Context) error {
	eng := s.engine()
	if eng == nil {
		return notRunning(&s.nodeBase)
	}
	if f, ok := eng.(folder); ok {
		return s.said(f.Fold(ctx), "", "")
	}
	return nil
}

// Erase erases every record whose subject is one of ids, in every store of
// the app, as its retention would — or deletes it where the store says
// kit.DeleteOnErasure — and folds the stores it wrote, so that what it
// overwrote leaves their files. A held record is left in place, and the
// result says so: it lists, per store, what was erased, deleted and held.
// Telling the recipients (GDPR art. 19) is the product's job; the register
// lists them. reason is journaled.
//
// Once sealing lands (ADR 0006, step 3), an erasure also moves the held
// records under keys of their own and destroys the subject's data key, so
// that it reaches every copy kit sealed.
func Erase(ctx context.Context, reason string, ids ...string) (Erasure, error) {
	a := appOf(ctx)
	if a == nil || !a.running() {
		return Erasure{}, Unavailable("kit.Erase runs inside a running app: call it from an endpoint, a job or a loop")
	}
	return a.erase(ctx, reason, ids)
}

// erase is kit.Erase for the app: what it erased, deleted and held, and
// what failed, store by store.
func (a *App) erase(ctx context.Context, reason string, ids []string) (Erasure, error) {
	out := Erasure{Stores: []model.StoreErasure{}}
	ids = identities(ids)
	if len(ids) == 0 {
		return out, Invalid("an erasure names at least one identity")
	}
	if strings.TrimSpace(reason) == "" {
		return out, Invalid("an erasure needs a reason, for the journal")
	}
	var failed []error
	for _, st := range a.productStores() {
		if st.plan().subject == nil {
			continue
		}
		sctx, sp := a.beginPrivacy(ctx, st.base().id, model.EdgeWrites, "erase", "Erase")
		part, err := a.eraseIn(sctx, st, reason, ids)
		sp.end(err)
		failed = append(failed, err)
		if len(part.Erased)+len(part.Deleted)+len(part.Held) > 0 {
			out.Stores = append(out.Stores, part)
		}
	}
	return out, errors.Join(failed...)
}

// eraseIn erases a person's records in one store, then folds it: after a
// failure too, when a record was written.
func (a *App) eraseIn(ctx context.Context, st privacyStore, reason string, ids []string) (model.StoreErasure, error) {
	part := model.StoreErasure{Store: st.base().id}
	keys, err := st.subjectKeys(ctx, a, ids)
	for _, key := range keys {
		var outcome erasureOutcome
		outcome, err = st.eraseOnRequest(ctx, a, key, reason)
		addOutcome(&part, outcome, key)
		if err != nil {
			break
		}
	}
	if len(part.Erased)+len(part.Deleted) > 0 {
		err = errors.Join(err, st.fold(ctx))
	}
	return part, err
}

// addOutcome lists key where its outcome says.
func addOutcome(part *model.StoreErasure, outcome erasureOutcome, key string) {
	switch outcome {
	case outcomeHeld:
		part.Held = append(part.Held, key)
	case outcomeDeleted:
		part.Deleted = append(part.Deleted, key)
	case outcomeErased:
		part.Erased = append(part.Erased, key)
	case outcomeNone:
		// Nothing was done to the record: no list names it.
	}
}
