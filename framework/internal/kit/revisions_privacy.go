// Package kit — what the privacy rules ask of a record's versions.
package kit

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/docstore"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// What ADR 0006 asks of a record's versions (ADR 0007 §4).
//
//   - Sealed: a version rests as its record rested, its sealed members boxes
//     under the record's data key, bound to the store, the record and the
//     member — the SDK copies the record's own bytes into its versions. A
//     person's erasure destroys their key, and every version sealed under it
//     stops opening in the same write.
//   - Erased: kit then rewrites the former versions as it rewrote the record
//     — each opened, cleared as the erasure cleared the record, sealed again
//     —, so that neither Restore nor the Studio can bring back an erased
//     value, whatever the key; a version kit cannot open or decode is
//     dropped. A deletion takes them all, in the record's own write.
//   - Held: a hold keeps them from pruning (versionsHeld), and moves them
//     under the record's own key with the record, so that its person's
//     erasure leaves them readable.
//   - Exported: a person's export carries their records' versions, without
//     their secret members.

// versionKeeper is the engine that keeps the store's versions, as they
// rest; false when the store keeps none, or does not run.
func (s *StoreService[T]) versionKeeper() (versionKeeper, bool) {
	if s.revisions == 0 {
		return nil, false
	}
	vk, ok := s.engine().(versionKeeper)
	return vk, ok
}

// eraseVersions rewrites the former versions of the record under key as an
// erasure rewrote the record: each opened, decoded, cleared by clear, and
// sealed again as the store seals, the members clear cleared left as they
// are. A version kit cannot open or decode is dropped: what cannot be
// cleared is not kept.
func (s *StoreService[T]) eraseVersions(ctx context.Context, key string, clear func(v *T)) error {
	vk, ok := s.versionKeeper()
	if !ok {
		return nil
	}
	se := s.sealing()
	ref, release, err := s.versionRef(ctx, se, key)
	if err != nil {
		return err
	}
	defer release()
	erasing := s.erasing(ctx, nil)
	err = vk.rewriteVersions(ctx, key, func(former []docstore.Version) ([]docstore.Version, error) {
		kept := former[:0]
		for _, v := range former {
			raw, ok := s.clearedVersion(erasing, se, key, ref, v.JSON, clear)
			if !ok {
				continue
			}
			v.JSON = raw
			kept = append(kept, v)
		}
		return kept, nil
	})
	return s.versionsRewritten(err)
}

// clearedVersion is one former version, raw as it rests, cleared by clear
// and resting again — sealed under ref, what clear left of the members kit
// seals —; false when kit cannot open or decode it.
func (s *StoreService[T]) clearedVersion(ctx context.Context, se *sealedEngine[T], key, ref string, raw []byte, clear func(*T)) ([]byte, bool) {
	plain := raw
	if se != nil {
		opened, err := se.openDoc(ctx, key, raw)
		if err != nil {
			return nil, false
		}
		plain = opened
	}
	var v T
	if json.Unmarshal(plain, &v) != nil {
		return nil, false
	}
	clear(&v)
	out, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	if se == nil || ref == "" {
		return out, true
	}
	sealed, err := se.seal(ctx, out, key, ref, se.clearNow())
	if err != nil {
		return nil, false
	}
	return sealed, true
}

// versionRef is the data key the record under key is sealed under now —
// what a rewrite of its versions seals what it keeps under —, with its
// stripe held until release: "" and nothing to release for a store that
// seals nothing, or a record that is gone.
func (s *StoreService[T]) versionRef(ctx context.Context, se *sealedEngine[T], key string) (string, func(), error) {
	if se == nil || !s.plan().sealsAny() {
		return "", func() {}, nil
	}
	cur, err := se.Get(ctx, key)
	switch {
	case errors.Is(err, docstore.DocumentNotFound):
		return "", func() {}, nil
	case err != nil:
		return "", func() {}, s.said(err, key, "")
	}
	ref, err := se.refOf(ctx, key, cur)
	if err != nil {
		return "", func() {}, err
	}
	lock := se.z.locks.of(ref)
	lock.RLock()
	return ref, heldToTheEnd(ctx, lock.RUnlock), nil
}

// versionsRewritten is a rewrite's outcome as its caller reads it: a record
// gone meanwhile has nothing to rewrite, and a write that stands but was not
// confirmed stands.
func (s *StoreService[T]) versionsRewritten(err error) error {
	switch {
	case err == nil, errors.Is(err, docstore.DocumentNotFound), errors.Is(err, docstore.WriteUnconfirmed):
		return nil
	case errors.Is(err, docstore.StoreClosed):
		return notRunning(&s.nodeBase)
	}
	return explain(CodeRevisionWrite, "REVISIONS_REWRITE", "a record's versions could not be rewritten", err, errs.String("store", s.id))
}

// moveVersions seals again, under to, what the former versions of the record
// under key keep sealed under from: a held record's, moved away from its
// person before their key is destroyed, as its former values are
// (moveFormer). A box whose key is destroyed stays as it is: nothing opens
// it.
func (s *StoreService[T]) moveVersions(ctx context.Context, key, from, to string) error {
	vk, ok := s.versionKeeper()
	se := s.sealing()
	if !ok || se == nil {
		return nil
	}
	err := vk.rewriteVersions(ctx, key, func(former []docstore.Version) ([]docstore.Version, error) {
		for i, v := range former {
			moved, _, err := boxWalk(v.JSON, s.plan().rules, "", false, func(at memberAt, value []byte) ([]byte, bool, error) {
				if ref, _ := boxRef(value); ref != from {
					return value, false, nil
				}
				plain, err := se.z.open(ctx, value, s.id, key, at.pointer)
				switch {
				case errors.Is(err, errErased):
					return value, false, nil
				case err != nil:
					return nil, false, err
				}
				box, err := se.z.seal(ctx, to, plain, s.id, key, at.pointer)
				return box, false, err
			})
			if err != nil {
				return nil, err
			}
			former[i].JSON = moved
		}
		return former, nil
	})
	return s.versionsRewritten(err)
}

// versionsInClear reports whether a former version of the record under key
// keeps in clear a member kit seals — made before its field was sealed —:
// what `privacy seal` seals.
func (s *StoreService[T]) versionsInClear(ctx context.Context, key string) bool {
	vk, ok := s.versionKeeper()
	se := s.sealing()
	if !ok || se == nil {
		return false
	}
	all, err := vk.storedVersions(ctx, key)
	if err != nil || len(all) < 2 {
		return false
	}
	for _, v := range all[1:] {
		if s.holdsInClear(se, v.JSON) {
			return true
		}
	}
	return false
}

// holdsInClear reports whether raw, a record as it rests, keeps in clear a
// member kit seals, and holds something.
func (s *StoreService[T]) holdsInClear(se *sealedEngine[T], raw []byte) bool {
	found, plain := false, se.clearNow()
	_, _, err := sealWalk(raw, s.plan().rules, "", false, func(at memberAt, value []byte) ([]byte, bool, error) {
		kept := !at.inList && plain[at.pointer]
		found = found || (!isBoxValue(value) && !isNull(value) && !kept && !zeroJSON(value, at.f.typ))
		return value, false, nil
	})
	// A record the walk cannot read is sealed again by its next write.
	return found || err != nil
}

// sealVersions seals, under ref, what the former versions of the record under
// key keep in clear of the members kit seals; a member sealed already stays
// under its key.
func (s *StoreService[T]) sealVersions(ctx context.Context, key, ref string) error {
	vk, ok := s.versionKeeper()
	se := s.sealing()
	if !ok || se == nil {
		return nil
	}
	clear := se.clearNow()
	err := vk.rewriteVersions(ctx, key, func(former []docstore.Version) ([]docstore.Version, error) {
		for i, v := range former {
			sealed, _, err := sealWalk(v.JSON, s.plan().rules, "", false, func(at memberAt, value []byte) ([]byte, bool, error) {
				if isBoxValue(value) || isNull(value) || (!at.inList && clear[at.pointer]) {
					return value, false, nil
				}
				box, err := se.z.seal(ctx, ref, value, s.id, key, at.pointer)
				return box, false, err
			})
			if err != nil {
				return nil, err
			}
			former[i].JSON = sealed
		}
		return former, nil
	})
	return s.versionsRewritten(err)
}

// exportVersions are the versions of the record under key as its person
// receives them — each without its secret members, as deep as a record goes
// —, or as the Studio's preview shows them, every personal, special and
// secret value redacted. A store that keeps no revisions, or a record gone
// since the export read it, has none.
func (s *StoreService[T]) exportVersions(ctx context.Context, key string, preview bool) ([]model.RecordVersion, error) {
	if _, ok := s.versionKeeper(); !ok {
		return nil, nil
	}
	all, err := s.openedVersions(ctx, key)
	switch {
	case isNotFound(err):
		return nil, nil
	case err != nil:
		return nil, err
	}
	out := make([]model.RecordVersion, 0, len(all))
	for _, v := range all {
		value, err := s.exportedValue(v.JSON, preview)
		if err != nil {
			return nil, err
		}
		out = append(out, model.RecordVersion{Number: v.Number, At: v.At, By: v.Meta[metaBy], Command: v.Meta[metaCommand], Value: value})
	}
	return out, nil
}

// exportedValue is a version's value in an export: without its secret
// members, or redacted for the preview.
func (s *StoreService[T]) exportedValue(raw []byte, preview bool) (json.RawMessage, error) {
	if !preview {
		out, err := s.plan().withoutSecrets(raw)
		if err != nil {
			return nil, failure(CodeStoreEncode, "STORE_ENCODE", "the entity cannot be exported", err, errs.String("store", s.id))
		}
		return out, nil
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return placeholder, nil
	}
	shown, _ := redactValue(v, 64<<10)
	return shown, nil
}

// versionsErasable reports whether a former version of the record under key
// holds what an erasure clears: a record whose members are cleared already
// may still keep them in its versions. Versions kit cannot read are: the
// erasure tries, and drops what it cannot clear.
func (s *StoreService[T]) versionsErasable(ctx context.Context, key string) bool {
	if _, ok := s.versionKeeper(); !ok {
		return false
	}
	all, err := s.openedVersions(ctx, key)
	if err != nil {
		return !isNotFound(err)
	}
	plan := s.plan()
	for _, v := range all[min(1, len(all)):] {
		var t T
		if json.Unmarshal(v.JSON, &t) != nil || !plan.cleared(reflect.ValueOf(&t).Elem()) {
			return true
		}
	}
	return false
}
