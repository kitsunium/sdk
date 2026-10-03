// Package kit — what kit's own writes mean to the history.
package kit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// formerBinding is the last part a former value's box is bound to, after
// the store, the record and the field: a former value never opens as the
// field's value, nor the field's as a former one.
const formerBinding = "former"

// What a write does to a record's history (history.go).

// historyIntent is what a write of kit's own means to the history: an
// erasure, whose cleared fields take their former values with them, or a
// rehash, which stores the same password again.
type historyIntent struct {
	// erases reports whether the write clears the field at a pointer; nil
	// outside an erasure.
	erases func(pointer string) bool
	// same is the pointer of a field the write stores again with the same
	// meaning: it records no former value.
	same string
}

type historyIntentKey struct{}

// withIntent returns ctx carrying what its writes mean to the history.
func withIntent(ctx context.Context, in historyIntent) context.Context {
	return context.WithValue(ctx, historyIntentKey{}, in)
}

// intentOf is what ctx's writes mean to the history: a product's change,
// unless kit said otherwise.
func intentOf(ctx context.Context) historyIntent {
	in, _ := ctx.Value(historyIntentKey{}).(historyIntent)
	return in
}

// memberValue is one historied field of a record: its JSON, and whether it
// holds its type's zero value.
type memberValue struct {
	raw  json.RawMessage
	zero bool
}

// historyWrite is what one write did to a record's history, to put it back
// when the record's own write is refused.
type historyWrite struct {
	stored, existed bool
	before, after   historyRecord
	// unconfirmed is the history's WriteUnconfirmed: it stands, and the
	// record's write says so once it stands too.
	unconfirmed error
}

// updating is the engine's update of one record: fn's change, the password
// policies' check of what it changed, then the history of what changed,
// before the record.
func (h *historied[T]) updating(ctx context.Context, key string, v *T, fn func(*T) error, w *historyWrite) error {
	var before map[string]memberValue
	ref := ""
	if h.hist != nil {
		before = h.values(*v)
		var err error
		if ref, err = h.sealRef(ctx, key, *v); err != nil {
			return err
		}
	}
	hashes := h.s.passwordHashes(v)
	if err := fn(v); err != nil {
		return err
	}
	if err := h.checkPasswords(hashes, *v); err != nil {
		return err
	}
	if h.hist == nil || h.s.keyOf(*v) != key {
		return nil // the engine refuses an update that renames: nothing to remember
	}
	return h.record(ctx, key, ref, before, *v, w)
}

// sealRef is the data key the former values of was — the record under key
// as a write found it — are sealed under: the record's own, "" when the
// store seals nothing.
func (h *historied[T]) sealRef(ctx context.Context, key string, was T) (string, error) {
	if h.seal == nil || !h.s.plan().sealsAny() {
		return "", nil
	}
	return h.seal.refOf(ctx, key, was)
}

// values reads the historied fields of v.
func (h *historied[T]) values(v T) map[string]memberValue {
	rv, plan := reflect.ValueOf(&v).Elem(), h.s.plan()
	out := make(map[string]memberValue, len(h.keeps))
	for pointer := range h.keeps {
		if m := plan.member(pointer); m != nil {
			out[pointer] = valueAt(rv, m.path)
		}
	}
	return out
}

// valueAt is the field at path in rv: its JSON — null when a nil pointer
// stops the way to it — and whether it holds its type's zero value.
func valueAt(rv reflect.Value, path []step) memberValue {
	fv := fieldAt(rv, path)
	if !fv.IsValid() {
		return memberValue{raw: json.RawMessage("null"), zero: true}
	}
	target := fv.Interface()
	if fv.CanAddr() {
		target = fv.Addr().Interface()
	}
	raw, err := json.Marshal(target)
	if err != nil {
		raw = json.RawMessage("null")
	}
	return memberValue{raw: raw, zero: fv.IsZero()}
}

// record writes, before the record, what the write changes in its history:
// the former values of the fields it changed, or — for an erasure — the
// former values of the fields it clears, removed.
func (h *historied[T]) record(ctx context.Context, key, ref string, before map[string]memberValue, after T, w *historyWrite) error {
	in := intentOf(ctx)
	changed := changedFields(before, h.values(after), in.same)
	if len(changed) == 0 && in.erases == nil {
		return nil
	}
	doc, existed, err := h.load(ctx, key, CodeHistoryWrite)
	if err != nil {
		return err
	}
	next := historyRecord{Key: key, Fields: maps.Clone(doc.Fields)}
	if next.Fields == nil {
		next.Fields = map[string][]formerEntry{}
	}
	uid, _ := UserID(ctx)
	for _, p := range changed {
		list, err := h.pushed(ctx, formerAt{key: key, pointer: p, ref: ref}, next.Fields[p], before[p], formerEntry{Until: h.now(), By: string(uid)})
		if err != nil {
			return err
		}
		next.Fields[p] = list
	}
	if in.erases != nil {
		maps.DeleteFunc(next.Fields, func(p string, _ []formerEntry) bool { return in.erases(p) })
	}
	h.prune(ctx, key, after, &next)
	if sameHistory(doc, next) {
		return nil
	}
	*w = historyWrite{existed: existed, before: doc, after: next}
	return h.put(ctx, w)
}

// changedFields are the pointers of the fields whose JSON a write changed,
// in order, but for same's.
func changedFields(before, after map[string]memberValue, same string) []string {
	var out []string
	for p, was := range before {
		if p != same && !bytes.Equal(was.raw, after[p].raw) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// formerAt is where a field's former values lie: the record's key, the
// field's pointer, and the data key they are sealed under.
type formerAt struct {
	key, pointer, ref string
}

// pushed is the former values of the field at with the value a write
// replaced at their head, sealed under at's ref and stamped as stamp says. A
// head that equals it was left by a write that did not stand, and goes; a
// zero value is no former value.
func (h *historied[T]) pushed(ctx context.Context, at formerAt, list []formerEntry, was memberValue, stamp formerEntry) ([]formerEntry, error) {
	if len(list) > 0 {
		if head, err := h.fromHistory(ctx, at.key, at.pointer, list[0].Value); err == nil && bytes.Equal(head, was.raw) {
			list = list[1:]
		}
	}
	if was.zero {
		return list, nil
	}
	value, err := h.toHistory(ctx, at.key, at.pointer, at.ref, was.raw)
	if err != nil {
		return nil, err
	}
	stamp.Value = value
	return append([]formerEntry{stamp}, list...), nil
}

// prune keeps each field's newest former values, as many as it keeps —
// none for a field that no longer keeps any —, unless a hold keeps the
// record: then nothing is pruned until a write after the release.
func (h *historied[T]) prune(ctx context.Context, key string, v T, doc *historyRecord) {
	over := false
	for p, list := range doc.Fields {
		if len(list) == 0 {
			delete(doc.Fields, p)
			continue
		}
		over = over || len(list) > h.keeps[p]
	}
	if !over || h.held(ctx, key, v) {
		return
	}
	for p, list := range doc.Fields {
		switch n := h.keeps[p]; {
		case n == 0:
			delete(doc.Fields, p)
		case len(list) > n:
			doc.Fields[p] = list[:n:n]
		}
	}
}

// held reports whether a hold keeps the record — placed, or the store's
// HeldUntil —; when kit cannot tell, it is held: what a hold may keep is
// never pruned on a guess.
func (h *historied[T]) held(ctx context.Context, key string, v T) bool {
	a := h.s.app()
	if a == nil {
		return true
	}
	held, err := h.s.heldAt(ctx, a, key, v, a.clock.Now())
	return held || err != nil
}

// now is the app's time.
func (h *historied[T]) now() time.Time {
	if a := h.s.app(); a != nil {
		return a.clock.Now().UTC()
	}
	return time.Now().UTC()
}

// load reads the history under key, and whether there is one: an empty
// history when there is not.
func (h *historied[T]) load(ctx context.Context, key string, code errs.Code) (historyRecord, bool, error) {
	doc, err := h.hist.Get(ctx, key)
	switch {
	case errors.Is(err, docstore.DocumentNotFound):
		return historyRecord{Key: key}, false, nil
	case err != nil:
		return historyRecord{}, false, h.failed(code, err)
	}
	return doc, true, nil
}

// put stores the history a write made — or removes it once it keeps
// nothing — and says in w that it did, even unconfirmed: a refused record
// puts it back.
func (h *historied[T]) put(ctx context.Context, w *historyWrite) error {
	var err error
	if len(w.after.Fields) == 0 {
		err = h.hist.Delete(ctx, w.after.Key)
	} else {
		err = h.hist.Write(ctx, w.after, upsert)
	}
	switch {
	case errors.Is(err, docstore.WriteUnconfirmed):
		w.unconfirmed, err = err, nil
	case errors.Is(err, docstore.DocumentNotFound):
		err = nil
	}
	if err != nil {
		return h.failed(CodeHistoryWrite, err)
	}
	w.stored = true
	return nil
}

// toHistory is a former value of the field at pointer as the history keeps
// it: sealed under ref when the store seals the field — or the field it
// sits in —, as it is otherwise.
func (h *historied[T]) toHistory(ctx context.Context, key, pointer, ref string, raw json.RawMessage) (json.RawMessage, error) {
	if ref == "" || !h.s.plan().sealedAt(pointer) {
		return raw, nil
	}
	return h.seal.z.seal(ctx, ref, raw, h.s.id, key, pointer, formerBinding)
}

// fromHistory is a former value the history kept, as it was: opened when it
// rests sealed, as written otherwise — kept before its field was sealed. A
// value whose data key is destroyed is errErased: it is gone.
func (h *historied[T]) fromHistory(ctx context.Context, key, pointer string, raw json.RawMessage) (json.RawMessage, error) {
	if !isBoxValue(raw) {
		return raw, nil
	}
	if h.seal == nil {
		return nil, errErased // no key to open it with: gone as surely
	}
	return h.seal.z.open(ctx, raw, h.s.id, key, pointer, formerBinding)
}

// sealedAt reports whether kit seals the field at pointer, or a field it
// sits in.
func (p *classPlan) sealedAt(pointer string) bool {
	return slices.ContainsFunc(p.members, func(m member) bool {
		return m.tag.sealedAtRest() && within(pointer, m.pointer)
	})
}

// sealFormer seals, under ref, the former values of the record under key
// that a sealed field kept in clear — written before it was sealed —: what
// the privacy command seals. A value already sealed is left under its key.
func (h *historied[T]) sealFormer(ctx context.Context, key, ref string) error {
	return h.rewriteFormer(ctx, key, func(p string, value json.RawMessage) (json.RawMessage, error) {
		if isBoxValue(value) || zeroJSON(value, nil) {
			return value, nil
		}
		return h.toHistory(ctx, key, p, ref, value)
	})
}

// moveFormer seals again, under to, the former values of the record under
// key sealed under from: a held record's, moved away from its person
// before their key is destroyed. A value whose key is destroyed stays as it
// is: nothing opens it.
func (h *historied[T]) moveFormer(ctx context.Context, key, from, to string) error {
	return h.rewriteFormer(ctx, key, func(p string, value json.RawMessage) (json.RawMessage, error) {
		if ref, _ := boxRef(value); ref != from {
			return value, nil
		}
		v, err := h.fromHistory(ctx, key, p, value)
		switch {
		case errors.Is(err, errErased):
			return value, nil
		case err != nil:
			return nil, err
		}
		return h.toHistory(ctx, key, p, to, v)
	})
}

// rewriteFormer writes the history of the record under key again, each
// former value of a sealed field as fn returns it.
func (h *historied[T]) rewriteFormer(ctx context.Context, key string, fn func(pointer string, value json.RawMessage) (json.RawMessage, error)) error {
	if h.hist == nil || h.seal == nil {
		return nil
	}
	plan := h.s.plan()
	_, err := h.hist.Update(ctx, key, func(doc *historyRecord) error {
		for p, list := range doc.Fields {
			if !plan.sealedAt(p) {
				continue
			}
			for i, e := range list {
				v, err := fn(p, e.Value)
				if err != nil {
					return err
				}
				list[i].Value = v
			}
		}
		return nil
	})
	switch {
	case err == nil, errors.Is(err, docstore.DocumentNotFound), errors.Is(err, docstore.WriteUnconfirmed):
		return nil
	}
	return h.failed(CodeHistoryWrite, err)
}

// inClear reports whether the history of the record under key keeps a
// former value of a sealed field in clear: kept before it was sealed.
func (h *historied[T]) inClear(ctx context.Context, key string) bool {
	if h.hist == nil || h.seal == nil {
		return false
	}
	doc, _, err := h.load(ctx, key, CodeHistoryRead)
	if err != nil {
		return false
	}
	plan := h.s.plan()
	for p, list := range doc.Fields {
		for _, e := range list {
			if plan.sealedAt(p) && !isBoxValue(e.Value) && !zeroJSON(e.Value, nil) {
				return true
			}
		}
	}
	return false
}
