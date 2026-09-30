// Package kit — legal holds: records kept whatever their retention says.
package kit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/model"
)

// Legal holds (ADR 0006 §5). A hold stops erasure and deletion, whoever
// asks: the retention, a person's erasure, the product's own Delete. It is
// the CNIL's intermediate archive — kept while a legal obligation or a
// dispute justifies it — and GDPR art. 17(3)(b) and (e).
//
//   - Store.Hold and Store.Release hold one record: a case under litigation,
//     an authority's request.
//   - kit.HoldSubject holds every record a person has, in every store, now.
//   - kit.HeldUntil, a store option, holds each record until an instant it
//     carries: the law's own retention.
//
// A held record is exported and updated like any other, and neither erased
// nor deleted: Delete answers a Conflict that names the hold — who placed it
// and when; for HeldUntil, the instant and the legal ground the store
// declares, which the register publishes —, never a placed hold's reason. A
// hold records who placed it and when; its reason is kept for whoever runs
// the product, tagged secret — the Studio never shows it, and kit seals it
// at rest. Holds are kept in kit's own store, kit.privacy/store/holds, one
// per record, filed under the record's reference.
//
// Placing a hold, and checking for one before an erasure or a deletion,
// happen one at a time per store (lockHolds): a hold placed while a record is
// erased waits for the erasure, and an erasure that starts after it sees it.
// A held record is sealed at rest under a data key of its own, from its hold
// on: its person's erasure destroys their key, never it.
// When kit cannot tell whether a record is held — the holds or the index key
// cannot be read —, nothing is erased or deleted: the caller gets the
// error.

// holdRecord is one held record, in kit's own store.
type holdRecord struct {
	// ID is the record's reference: its store and its key, keyed.
	ID string `json:"id"`
	// Store is the store's node ID.
	Store string `json:"store"`
	// Key is the record's key, to find it again. A key may be personal.
	Key string `json:"key" kit:"personal"`
	// Subject is the reference of the person the record is about.
	Subject string `json:"subject,omitempty"`
	// Reason is why, as the caller said it.
	Reason string `json:"reason" kit:"secret"`
	// By is who placed it, and At when.
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// key is the hold's key in kit's store.
func (h *holdRecord) key() string { return h.ID }

// Hold places a legal hold on the record under key: neither the retention,
// nor a person's erasure, nor Delete touches it until [StoreService.Release] lifts
// it. reason says why — an authority's request, a case under litigation —
// and is kept for whoever runs the product, never shown in the Studio.
// Holding a held record again replaces its reason.
func (s *StoreService[T]) Hold(ctx context.Context, key, reason string) error {
	a := s.app()
	if a == nil {
		return notRunning(&s.nodeBase)
	}
	ctx, sp := a.beginPrivacy(ctx, s.id, model.EdgeWrites, "", "Hold")
	err := s.holdKey(ctx, a, key, reason)
	sp.end(err)
	return err
}

// Release lifts the hold on the record under key: the retention takes it up
// again, from where its instants say. A record no hold keeps is a
// [NotFound] error.
func (s *StoreService[T]) Release(ctx context.Context, key string) error {
	a := s.app()
	if a == nil {
		return notRunning(&s.nodeBase)
	}
	ctx, sp := a.beginPrivacy(ctx, s.id, model.EdgeWrites, "", "Release")
	err := s.release(ctx, a, key)
	sp.end(err)
	return err
}

// HoldSubject places a legal hold on every record whose subject is one of
// ids, in every store of the app: an authority's request about a person.
// It holds the records they have now; a record written later is not held.
// reason is kept as Store.Hold keeps it.
func HoldSubject(ctx context.Context, reason string, ids ...string) error {
	a := appOf(ctx)
	if a == nil || !a.running() {
		return Unavailable("kit.HoldSubject runs inside a running app: call it from an endpoint, a job or a loop")
	}
	return a.holdSubject(ctx, reason, ids)
}

// holdSubject is kit.HoldSubject for the app.
func (a *App) holdSubject(ctx context.Context, reason string, ids []string) error {
	ids = identities(ids)
	if len(ids) == 0 {
		return Invalid("a hold names at least one identity")
	}
	var failed []error
	for _, st := range a.productStores() {
		if st.plan().subject != nil {
			failed = append(failed, a.holdSubjectIn(ctx, st, reason, ids))
		}
	}
	return errors.Join(failed...)
}

// holdSubjectIn holds a person's records in one store, in a span of its own
// on the store's node.
func (a *App) holdSubjectIn(ctx context.Context, st privacyStore, reason string, ids []string) error {
	ctx, sp := a.beginPrivacy(ctx, st.base().id, model.EdgeWrites, "hold", "Hold")
	keys, err := st.subjectKeys(ctx, a, ids)
	for _, key := range keys {
		if err != nil {
			break
		}
		err = st.holdKey(ctx, a, key, reason)
	}
	sp.end(err)
	return err
}

// holdKey holds the record under key.
func (s *StoreService[T]) holdKey(ctx context.Context, a *App, key, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return Invalid("a hold needs a reason")
	}
	holds, _ := a.privacyStores()
	if holds == nil {
		return noPrivacy()
	}
	// The holds' writer turn before the hold lock: a write's outermost lock
	// (transact_turn.go).
	ctx, release, err := holds.turn(ctx)
	defer release()
	if err != nil {
		return err
	}
	unlock := a.lockHolds(s.id)
	defer unlock()
	v, err := s.read(ctx, key)
	if err != nil {
		return err
	}
	keys, err := a.referenceKeys(ctx)
	if err != nil {
		return err
	}
	h := holdRecord{ID: keys.recordRef(s.id, key), Store: s.id, Key: key, Reason: reason, By: actorOf(ctx), At: a.clock.Now().UTC()}
	if id := s.subjectOf(v); id != "" {
		h.Subject = keys.subjectRef(id)
	}
	if err := holds.write(ctx, h, upsert); err != nil {
		return err
	}
	s.reschedule(key)
	// Held, the record rests under a data key of its own from now on: its
	// person's erasure leaves it readable (seal.go).
	moved := s.moveOwn(ctx, a, key)
	return errors.Join(moved, a.journal(ctx, &journalLine{op: model.JournalHold, store: s.id, key: key, subject: s.subjectOf(v), reason: reason}))
}

// release lifts the hold on the record under key.
func (s *StoreService[T]) release(ctx context.Context, a *App, key string) error {
	holds, _ := a.privacyStores()
	if holds == nil {
		return noPrivacy()
	}
	keys, err := a.referenceKeys(ctx)
	if err != nil {
		return err
	}
	h, err := holds.read(ctx, keys.recordRef(s.id, key))
	if isNotFound(err) {
		return NotFound(fmt.Sprintf("%s: no hold keeps the record %q", s.name, clip(key)))
	}
	if err != nil {
		return err
	}
	if err := holds.remove(ctx, h.ID); err != nil {
		return err
	}
	s.reschedule(key)
	var subject string
	if v, err := s.read(ctx, key); err == nil {
		subject = s.subjectOf(v)
	}
	return a.journal(ctx, &journalLine{op: model.JournalRelease, store: s.id, key: key, subject: subject})
}

// noPrivacy is the answer of a hold asked of an app that keeps no personal
// data: kit keeps no holds for it.
func noPrivacy() error {
	return Unavailable("this app keeps no personal data, so kit keeps no holds for it: classify the fields that hold some (ADR 0006)")
}

// lockHolds takes the store's hold lock, and returns what releases it. An
// app that keeps no holds locks nothing.
func (a *App) lockHolds(store string) (unlock func()) {
	if holds, _ := a.privacyStores(); holds == nil {
		return func() {}
	}
	m, _ := a.privacy.holdLocks.LoadOrStore(store, &sync.Mutex{})
	mu, isMutex := m.(*sync.Mutex)
	if !isMutex {
		mu = &sync.Mutex{}
		a.privacy.holdLocks.Store(store, mu)
	}
	mu.Lock()
	return mu.Unlock
}

// holdOf returns the hold on a record, and whether one keeps it; an error
// says kit cannot tell.
func (a *App) holdOf(ctx context.Context, store, key string) (holdRecord, bool, error) {
	holds, _ := a.privacyStores()
	if holds == nil {
		return holdRecord{}, false, nil
	}
	if holds.engine() == nil {
		return holdRecord{}, false, notRunning(&holds.nodeBase)
	}
	keys, err := a.referenceKeys(ctx)
	if err != nil {
		return holdRecord{}, false, err
	}
	h, err := holds.read(ctx, keys.recordRef(store, key))
	if isNotFound(err) {
		return holdRecord{}, false, nil
	}
	return h, err == nil, err
}

// holdRefusal is the Conflict a held record answers to what would erase or
// delete it: it names the hold — who placed it and when, or the instant the
// store holds it until and the legal ground it declares —, never a placed
// hold's reason. When kit cannot tell whether the record is held, it is the
// error that keeps it from telling: the caller stops as surely.
func (s *StoreService[T]) holdRefusal(ctx context.Context, a *App, key string) error {
	h, held, err := a.holdOf(ctx, s.id, key)
	switch {
	case err != nil:
		return err
	case held:
		return Conflict(fmt.Sprintf("%s: the record %q is on legal hold, placed by %s on %s: release it first",
			s.name, clip(key), clip(h.By), h.At.Format(time.DateOnly)))
	}
	return s.heldUntilRefusal(ctx, a, key)
}

// heldUntilRefusal is the Conflict of a record the store's HeldUntil holds
// at the app's now.
func (s *StoreService[T]) heldUntilRefusal(ctx context.Context, a *App, key string) error {
	p := s.privacy
	if p == nil || p.heldUntil == nil {
		return nil
	}
	v, err := s.read(ctx, key)
	switch {
	case isNotFound(err):
		return nil // a record that is not there holds nothing: the caller says so
	case err != nil:
		return err
	}
	if until, held := s.heldUntil(v); held && until.After(a.clock.Now()) {
		return Conflict(fmt.Sprintf("%s: the record %q is held until %s (%s)",
			s.name, clip(key), until.UTC().Format(time.DateOnly), clip(p.heldReason)))
	}
	return nil
}

// removeUnlessHeld removes the record under key unless a hold keeps it:
// Store.Delete's hook.
func (s *StoreService[T]) removeUnlessHeld(ctx context.Context, a *App, key string) error {
	unlock := a.lockHolds(s.id)
	defer unlock()
	if err := s.holdRefusal(ctx, a, key); err != nil {
		return err
	}
	return s.remove(ctx, key)
}

// heldCount is how many of the store's records a hold keeps, for the graph.
func (s *StoreService[T]) heldCount(a *App) int {
	all, err := a.allHolds(context.Background())
	if err != nil {
		return 0
	}
	n := 0
	for _, h := range all {
		if h.Store == s.id {
			n++
		}
	}
	return n
}

// allHolds are every hold kit keeps; none when the app keeps no personal
// data.
func (a *App) allHolds(ctx context.Context) ([]holdRecord, error) {
	holds, _ := a.privacyStores()
	if holds == nil {
		return nil, nil
	}
	if holds.engine() == nil {
		return nil, notRunning(&holds.nodeBase)
	}
	return holds.all(ctx)
}

// holdList is every hold, newest first, as the model says it: references
// only.
func (a *App) holdList(ctx context.Context) ([]model.Hold, error) {
	all, err := a.allHolds(ctx)
	if err != nil || all == nil {
		return nil, err
	}
	out := make([]model.Hold, 0, len(all))
	for _, h := range all {
		out = append(out, model.Hold{Store: h.Store, Record: h.ID, Subject: h.Subject, By: h.By, At: h.At})
	}
	slices.SortStableFunc(out, func(x, y model.Hold) int { return y.At.Compare(x.At) })
	return out, nil
}
