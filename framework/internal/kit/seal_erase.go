// Package kit — cryptographic erasure: a data key destroyed.
package kit

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/kitsunium/sdk/framework/model"
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
	if keys == nil || hold(ctx, heldEffect{node: keys.id, inside: true, release: func() error {
		return a.shredPeople(withoutUnit(context.WithoutCancel(ctx)), ids, refs)
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

// shredIfLast destroys the data key of identity once no store keeps a
// record of theirs: a retention's erasure or deletion, an erasure of one
// record, took their last. Under the key's stripe, a write sealing under it
// meanwhile either stood before — and keeps it — or makes a new one after.
func (a *App) shredIfLast(ctx context.Context, identity string) error {
	keys := a.privacyKeyStore()
	if identity == "" || keys == nil || hold(ctx, heldEffect{node: keys.id, inside: true, release: func() error {
		return a.shredIfLast(withoutUnit(context.WithoutCancel(ctx)), identity)
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
	last, err := a.lastOf(ctx, identity)
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

// lastOf reports whether no store of the product keeps a record whose
// subject is identity.
func (a *App) lastOf(ctx context.Context, identity string) (bool, error) {
	for _, st := range a.productStores() {
		if st.plan().subject == nil {
			continue
		}
		left, err := st.subjectKeys(ctx, a, []string{identity})
		if err != nil || len(left) > 0 {
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
