package app

import (
	"cmp"
	"context"
	"errors"
	"slices"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// The references at rest, re-sealed (ADR 0180 step 5c-2). At every start,
// once the stores are open, a pass moves what a former release sealed under
// a reference made with formerLabels — or bound to kit.privacy's ids — to
// the present ones (renamed_labels.go):
//
//   - a hold filed under its record's former reference is filed again under
//     the present one, its subject's reference with it when the record still
//     names one, and the former is removed, its own data key with it;
//   - a record a box of which names its person's former data key or its own
//     former one is written again, which seals it under the present one; its
//     former values and its versions sealed under either are opened and
//     sealed again under the present key of the same kind — a record in the
//     trash's versions too.
//
// The mark of the progress is what rests: a box names the key it is under, a
// hold the reference it is filed under. A record whose boxes all name
// present keys is not written; a hold filed under its present reference is
// not touched. A pass stopped anywhere — a crash, a refused write — leaves
// everything readable, under one key or the other, and the next start moves
// the rest; a second pass moves nothing.
//
// What the pass cannot reach is read where it lies: a message kept in a queue
// or a dead letter, sealed under a person's former key; a former value or a
// version sealed under the former key of another person than the record's
// own — whose identity the record no longer says. The former keys therefore
// stay, never written under: an erasure destroys a person's two keys, the
// last record of a person takes both, a record gone takes its two own
// (personRefs, ownRefs). While a record is moved, the stripes of its keys are
// held shared: an erasure that would destroy one waits for the move, then
// destroys what the move made — never the other way round, so no key is made
// again for a person once theirs is destroyed.

// addRelabelComponent registers the pass, after the stores open: its start
// runs it, once. What it could not move is said on the app's log, and the
// start goes on: everything still reads.
func (a *App) addRelabelComponent() error {
	if !a.keepsPersonalData() && !a.keepsDataKeys() {
		return nil
	}
	return a.component("relabel", "", func(ctx context.Context) error {
		a.relabel(ctx)
		return nil
	}, func(context.Context) error { return nil })
}

// relabel runs the pass over the holds, then every store of the product,
// and says what it moved.
func (a *App) relabel(ctx context.Context) {
	moved, err := a.relabelHolds(ctx)
	for _, st := range a.productStores() {
		n, serr := st.relabel(ctx)
		moved, err = moved+n, errors.Join(err, serr)
	}
	if a.log == nil {
		return
	}
	if moved > 0 {
		logger.Info(ctx, a.log, "what a former release sealed under its references is sealed under the present ones (ADR 0180 step 5c)",
			logger.Int("moved", moved))
	}
	if err != nil {
		logger.Warn(ctx, a.log, "some of what a former release sealed could not be sealed again: it reads where it lies, and the next start moves it",
			logger.String("error", errs.PublicOf(err)))
	}
}

// relabelHolds files every hold a former release filed under its record's
// former reference under the present one, and says how many.
func (a *App) relabelHolds(ctx context.Context) (int, error) {
	holds, _ := a.privacyStores()
	if holds == nil || holds.engine() == nil {
		return 0, nil
	}
	all, err := holds.all(ctx)
	if err != nil {
		return 0, err
	}
	keys, err := a.derivedKeys(ctx)
	if err != nil {
		return 0, err
	}
	moved := 0
	var failed []error
	for _, h := range all {
		if h.Key == "" || h.ID == keys.present.recordRef(h.Store, h.Key) {
			// Filed under its present reference already — or a hold whose
			// record's key no longer opens, its own data key destroyed since
			// (a backup restored after a pass): it is found under the
			// reference it is filed under, and stays there.
			continue
		}
		err := a.relabelHold(ctx, holds, keys, h)
		if err == nil {
			moved++
		}
		failed = append(failed, err)
	}
	return moved, errors.Join(failed...)
}

// relabelHold files h, a hold filed under its record's former reference,
// under the present one — unless a run before did —, then removes it: the
// new one is written before the former goes, so that the record is held all
// along. Under the holds' writer turn and the store's hold lock, as a hold
// placed is.
func (a *App) relabelHold(ctx context.Context, holds *StoreService[holdRecord], keys *derivedKeys, h holdRecord) error {
	ctx, release, err := holds.turn(ctx)
	defer release()
	if err != nil {
		return err
	}
	unlock := a.lockHolds(h.Store)
	defer unlock()
	moved := h
	moved.ID = keys.present.recordRef(h.Store, h.Key)
	switch _, err := holds.read(ctx, moved.ID); {
	case isNotFound(err):
		if subject, known := a.subjectOfRecord(ctx, h.Store, h.Key); known && subject != "" {
			moved.Subject = keys.present.subjectRef(subject)
		}
		if err := holds.write(ctx, moved, plug.WriteInsert); err != nil {
			return err
		}
	case err != nil:
		return err
	}
	return a.dropHold(ctx, holds, h.ID)
}

// subjectOfRecord is the subject of the record under key in the product's
// store id, and whether the framework knows it.
func (a *App) subjectOfRecord(ctx context.Context, id, key string) (string, bool) {
	for _, st := range a.productStores() {
		if st.base().id != id {
			continue
		}
		if sc, ok := st.(interface {
			subjectAt(ctx context.Context, a *App, key string) (string, bool)
		}); ok {
			return sc.subjectAt(ctx, a, key)
		}
	}
	return "", false
}

// dropFormerHold removes the hold a former release filed the record under key
// in the store id under, when there is one: a hold placed or lifted since
// replaces it.
func (a *App) dropFormerHold(ctx context.Context, id, key string) error {
	holds, _ := a.privacyStores()
	if holds == nil {
		return nil
	}
	keys, err := a.derivedKeys(ctx)
	if err != nil {
		return err
	}
	former := keys.former.recordRef(id, key)
	if former == keys.present.recordRef(id, key) {
		return nil
	}
	return a.dropHold(ctx, holds, former)
}

// dropHold removes the hold filed under ref, when there is one, and with it
// its own data keys (dropOwnKey).
func (a *App) dropHold(ctx context.Context, holds *StoreService[holdRecord], ref string) error {
	if err := holds.remove(ctx, ref); err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

// presentHolds are all, one hold per record: where a pass stopped between
// filing a hold under its present reference and removing the former one,
// the present one.
func (a *App) presentHolds(ctx context.Context, all []holdRecord) ([]holdRecord, error) {
	keys, err := a.derivedKeys(ctx)
	if err != nil {
		return nil, err
	}
	present := map[string]bool{}
	for _, h := range all {
		if h.ID == keys.present.recordRef(h.Store, h.Key) {
			present[h.Store+"\x00"+h.Key] = true
		}
	}
	return slices.DeleteFunc(all, func(h holdRecord) bool {
		return present[h.Store+"\x00"+h.Key] && h.ID != keys.present.recordRef(h.Store, h.Key)
	}), nil
}

// A store's part --------------------------------------------------------------

// keyMove is a data key a former release named, and the present one what it
// sealed moves to.
type keyMove struct{ from, to string }

// relabel moves what the store keeps under a former reference to the present
// one, record by record, then folds it: what was under the former leaves its
// files. It says how many records it moved.
func (s *storeCore) relabel(ctx context.Context) (int, error) {
	se := s.sealing()
	if se == nil || se.z == nil || !s.plan().sealsAny() {
		return 0, nil
	}
	keys, err := keptKeys(ctx, se)
	if err != nil {
		return 0, s.said(err, "", "")
	}
	moved := 0
	var failed []error
	for _, key := range keys {
		done, err := s.relabelOne(ctx, se, key)
		if done {
			moved++
		}
		failed = append(failed, err)
	}
	if moved > 0 {
		failed = append(failed, s.fold(ctx))
	}
	return moved, errors.Join(failed...)
}

// relabelOne moves what the record under key keeps under a former
// reference — itself, its former values, its versions —, and reports whether
// it moved anything. The stripes of every key involved are held shared while
// it does.
func (s *storeCore) relabelOne(ctx context.Context, se *sealedEngine, key string) (bool, error) {
	subject, live, err := s.subjectKept(ctx, se, key)
	if err != nil {
		return false, err
	}
	moves := keyMoves(se.z.dual, s.id, key, subject)
	var froms, all []string
	for _, m := range moves {
		froms, all = append(froms, m.from), append(all, m.from, m.to)
	}
	record, former, versions, err := s.underAny(ctx, se, key, live, froms)
	if err != nil || (!record && !former && !versions) {
		return false, err
	}
	release := se.z.shared(all...)
	defer release()
	return true, s.applyMoves(ctx, key, moves, record, former, versions)
}

// keyMoves are the moves of the record under key in the store id: its own
// former key to its own present one, and — when it names a subject — the
// subject's former key to their present one.
func keyMoves(dual *derivedKeys, id, key, subject string) []keyMove {
	moves := []keyMove{{from: dual.former.ownRef(sealedAs(id), key), to: dual.present.ownRef(id, key)}}
	if subject != "" {
		moves = append(moves, keyMove{from: dual.former.personRef(subject), to: dual.present.personRef(subject)})
	}
	return moves
}

// applyMoves moves the parts of the record under key that keep a box under
// a former key: the record, written again; its former values and versions,
// sealed again move by move.
func (s *storeCore) applyMoves(ctx context.Context, key string, moves []keyMove, record, former, versions bool) error {
	if record {
		// A write that changes nothing: the engine seals what it writes, under
		// the present key.
		if _, err := s.engine().Update(ctx, key, func(*plug.DocumentValue) error { return nil }); err != nil && !plug.Refused(err, plug.RefusalNotFound) {
			return s.said(err, key, "")
		}
	}
	var failed []error
	for _, m := range moves {
		if h := s.historied(); former && h != nil {
			failed = append(failed, h.moveFormer(ctx, key, m.from, m.to))
		}
		if versions {
			failed = append(failed, s.moveVersions(ctx, key, m.from, m.to))
		}
	}
	return errors.Join(failed...)
}

// subjectKept is the subject of the record under key — its newest version's
// for a record in the trash —, and whether the record is live. A trashed
// record whose newest version does not open has none the pass can name.
func (s *storeCore) subjectKept(ctx context.Context, se *sealedEngine, key string) (subject string, live bool, err error) {
	d, err := se.Get(ctx, key)
	switch {
	case err == nil:
		return s.subjectOf(d.Value), true, nil
	case !plug.Refused(err, plug.RefusalNotFound):
		return "", false, s.said(err, key, "")
	}
	versions, err := se.StoredVersions(ctx, key)
	if err != nil || len(versions) == 0 {
		return "", false, nil
	}
	newest := slices.MaxFunc(versions, func(x, y plug.VersionValue) int { return cmp.Compare(x.Number, y.Number) })
	doc, err := se.openDoc(ctx, key, newest.JSON)
	if err != nil {
		return "", false, nil
	}
	v, err := s.decodeEntity(doc)
	if err != nil {
		return "", false, nil
	}
	return s.subjectOf(v), false, nil
}

// underAny reports which parts of the record under key hold a box sealed
// under one of refs: the record itself — when it is live —, its former
// values, its versions.
func (s *storeCore) underAny(ctx context.Context, se *sealedEngine, key string, live bool, refs []string) (record, former, versions bool, err error) {
	if live {
		at, err := se.refsAt(ctx, key)
		if err != nil && !plug.Refused(err, plug.RefusalNotFound) {
			return false, false, false, s.said(err, key, "")
		}
		record = slices.ContainsFunc(at, func(r string) bool { return slices.Contains(refs, r) })
	}
	if former, err = s.formerUnderAny(ctx, key, refs); err != nil {
		return false, false, false, err
	}
	if !live {
		// A record in the trash keeps its versions as they rest: the SDK
		// rewrites no deleted document's. They read where they lie until the
		// record is restored — and written again — or purged.
		return record, former, false, nil
	}
	versions, err = s.versionsUnderAny(ctx, se, key, refs)
	return record, former, versions, err
}

// formerUnderAny reports whether a former value of the record under key is
// sealed under one of refs.
func (s *storeCore) formerUnderAny(ctx context.Context, key string, refs []string) (bool, error) {
	h := s.historied()
	if h == nil || h.hist == nil {
		return false, nil
	}
	d, err := h.hist.Get(ctx, key)
	switch {
	case plug.Refused(err, plug.RefusalNotFound):
		return false, nil
	case err != nil:
		return false, s.said(err, key, "")
	}
	rec, err := historyOf(d)
	if err != nil {
		return false, err
	}
	for _, entries := range rec.Fields {
		for _, e := range entries {
			if under, err := sealedUnderAny(e.Value, refs); err != nil || under {
				return under, err
			}
		}
	}
	return false, nil
}

// versionsUnderAny reports whether a version of the record under key is
// sealed under one of refs.
func (s *storeCore) versionsUnderAny(ctx context.Context, se *sealedEngine, key string, refs []string) (bool, error) {
	if _, ok := s.versionKeeper(); !ok {
		return false, nil
	}
	versions, err := se.StoredVersions(ctx, key)
	switch {
	case plug.Refused(err, plug.RefusalNotFound):
		return false, nil
	case err != nil:
		return false, s.said(err, key, "")
	}
	for _, v := range versions {
		if under, err := sealedUnderAny(v.JSON, refs); err != nil || under {
			return under, err
		}
	}
	return false, nil
}

// sealedUnderAny reports whether raw holds a box sealed under one of refs.
func sealedUnderAny(raw []byte, refs []string) (bool, error) {
	for _, ref := range refs {
		if under, err := sealedUnder(raw, ref); err != nil || under {
			return under, err
		}
	}
	return false, nil
}
