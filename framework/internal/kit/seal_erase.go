package kit

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// Cryptographic erasure (ADR 0006 §4, §6): a data key destroyed, every box it
// sealed stops opening wherever it was copied. Each destruction is a span on
// kit's own store of data keys, Shred; a person's is an entry of the privacy
// journal, shred, which names their reference only. kit's store of keys is
// folded after: a destroyed key leaves its files, not only its index. An
// erasure inside a transaction ([Transact]) destroys its keys once the
// transaction commits: a rollback puts back what it erased, sealed under
// them.

// shredPeople destroys the data keys of ids — a person's erasure — and the
// data keys refs name — those the erased records were sealed under —, then
// folds kit's store of keys. An app that keeps no data keys destroys
// nothing: nothing of it is sealed.
func (a *App) shredPeople(ctx context.Context, ids []string, refs map[string]bool) error {
	keys := a.privacyKeyStore()
	if keys == nil || hold(ctx, heldEffect{node: keys.id, inside: true, release: func(rctx context.Context) error {
		return a.shredPeople(context.WithoutCancel(rctx), ids, refs)
	}}) {
		return nil
	}
	z, err := a.sealing(ctx)
	if err != nil {
		return err
	}
	ctx, sp := a.beginPrivacy(ctx, keys.id, model.EdgeWrites, "shred", "Shred")
	people := map[string]string{}
	for _, id := range ids {
		ref := z.refs.personRef(id)
		people[ref], refs[ref] = id, true
	}
	var failed []error
	shredded := false
	for _, ref := range slices.Sorted(maps.Keys(refs)) {
		done, err := a.destroyKey(ctx, z, ref)
		failed = append(failed, err)
		shredded = shredded || done
		if id, person := people[ref]; person && done {
			failed = append(failed, a.journal(ctx, &journalLine{op: model.JournalShred, store: keys.id, subject: id}))
		}
	}
	if shredded {
		failed = append(failed, keys.fold(ctx))
	}
	err = errors.Join(failed...)
	sp.end(err)
	return err
}

// shredIfLast destroys the data key of identity once nothing kit keeps
// still needs it (lastOf): a retention's erasure or deletion, an erasure of
// one record, or the product's own Delete took their last record. Under the
// key's stripe, a write sealing under it
// meanwhile either stood before — and keeps it — or makes a new one after.
func (a *App) shredIfLast(ctx context.Context, identity string) error {
	keys := a.privacyKeyStore()
	if identity == "" || keys == nil || hold(ctx, heldEffect{node: keys.id, inside: true, release: func(rctx context.Context) error {
		return a.shredIfLast(context.WithoutCancel(rctx), identity)
	}}) {
		return nil
	}
	z, err := a.sealing(ctx)
	if err != nil {
		return err
	}
	ref := z.refs.personRef(identity)
	release, err := z.alone(ctx, ref)
	if err != nil {
		return err
	}
	last, err := a.lastOf(ctx, z, identity)
	if err != nil || !last {
		release()
		return err
	}
	sctx, sp := a.beginPrivacy(ctx, keys.id, model.EdgeWrites, "shred", "Shred")
	done, err := z.destroy(sctx, ref)
	// The stripe is released before the journal, which seals under keys of
	// its own.
	release()
	if done {
		err = errors.Join(err, a.journal(sctx, &journalLine{op: model.JournalShred, store: keys.id, subject: identity}), keys.fold(sctx))
	}
	sp.end(err)
	return err
}

// lastOf reports whether nothing kit keeps still needs the data key of
// identity: no store of the product keeps a record whose subject is
// identity, no hold names them, and no former value nor version of a record
// — one whose subject changed since — rests sealed under their key. When
// kit cannot tell, the key stays.
func (a *App) lastOf(ctx context.Context, z *sealer, identity string) (bool, error) {
	for _, st := range a.productStores() {
		if st.plan().subject == nil {
			continue
		}
		left, err := st.subjectKeys(ctx, a, []string{identity})
		if err != nil || len(left) > 0 {
			return false, err
		}
	}
	all, err := a.allHolds(ctx)
	if err != nil {
		return false, err
	}
	subject := z.refs.subjectRef(identity)
	if slices.ContainsFunc(all, func(h holdRecord) bool { return h.Subject == subject }) {
		return false, nil
	}
	ref := z.refs.personRef(identity)
	for _, st := range a.productStores() {
		if kept, err := st.sealsUnder(ctx, ref); err != nil || kept {
			return false, err
		}
	}
	return true, nil
}

// destroyKey destroys the data key ref under its stripe alone, and reports
// whether one was held.
func (a *App) destroyKey(ctx context.Context, z *sealer, ref string) (bool, error) {
	release, err := z.alone(ctx, ref)
	if err != nil {
		return false, err
	}
	defer release()
	return z.destroy(ctx, ref)
}

// sealsUnder reports whether a record of the store keeps, in its former
// values or its versions, a value sealed under the data key ref: those of a
// record whose subject changed since stay sealed under its former person's
// key, which they still need. A store that seals nothing keeps none. A box
// whose key cannot be read is an error, never "not under ref": when kit
// cannot tell, the key stays.
func (s *StoreService[T]) sealsUnder(ctx context.Context, ref string) (bool, error) {
	se := s.sealing()
	if se == nil {
		return false, nil
	}
	if kept, err := s.formerSealUnder(ctx, ref); err != nil || kept {
		return kept, err
	}
	if s.revisions == 0 {
		return false, nil
	}
	return versionsSealUnder(ctx, se, ref)
}

// formerSealUnder reports whether a former value the store's history keeps
// is sealed under the data key ref.
func (s *StoreService[T]) formerSealUnder(ctx context.Context, ref string) (bool, error) {
	h := s.historied()
	if h == nil || h.hist == nil {
		return false, nil
	}
	docs, err := h.hist.List(ctx)
	if err != nil {
		return false, err
	}
	for _, d := range docs {
		for _, entries := range d.Fields {
			for _, e := range entries {
				if kept, err := sealedUnder(e.Value, ref); err != nil || kept {
					return kept, err
				}
			}
		}
	}
	return false, nil
}

// versionsSealUnder reports whether a version a record of se keeps is
// sealed under the data key ref.
func versionsSealUnder[T any](ctx context.Context, se *sealedEngine[T], ref string) (bool, error) {
	entries, err := se.resting(ctx, 0)
	if err != nil {
		return false, err
	}
	for _, en := range entries {
		versions, err := se.storedVersions(ctx, en.Key)
		switch {
		case errors.Is(err, docstore.DocumentNotFound):
			continue // deleted meanwhile, its versions with it
		case err != nil:
			return false, err
		}
		for _, v := range versions {
			if kept, err := sealedUnder(v.JSON, ref); err != nil || kept {
				return kept, err
			}
		}
	}
	return false, nil
}

// sealedUnder reports whether raw — a record, a version, a former value —
// holds a box sealed under the data key ref. A box whose data key cannot
// be read is an error: kit cannot tell.
func sealedUnder(raw []byte, ref string) (bool, error) {
	found := false
	visit := func(_ memberAt, value []byte) ([]byte, bool, error) {
		got, err := keyOfBox(value)
		found = found || (err == nil && got == ref)
		return value, false, err
	}
	if isBoxValue(raw) {
		_, _, err := visit(memberAt{}, raw)
		return found, err
	}
	_, _, err := boxWalk(raw, nil, "", false, visit)
	return found, err
}

// keyOfBox is the data key a box names: "" for one sealed under data-key
// itself, which names none, and an error for one whose key cannot be read.
func keyOfBox(value []byte) (string, error) {
	root, raw, ok := boxBytes(value)
	switch {
	case !ok:
		return "", failure(CodeSealOpen, "SEAL_BOX_UNREADABLE", "a sealed value cannot be read", nil)
	case root:
		return "", nil
	}
	ref, err := secret.SubjectOf(raw)
	if err != nil {
		return "", failure(CodeSealOpen, "SEAL_BOX_UNREADABLE", "a sealed value cannot be read", err)
	}
	return ref, nil
}
