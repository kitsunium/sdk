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

	"github.com/kitsunium/sdk/pkg/v1/docstore"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

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
	if h.hist != nil {
		before = h.values(*v)
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
	return h.record(ctx, key, before, *v, w)
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
func (h *historied[T]) record(ctx context.Context, key string, before map[string]memberValue, after T, w *historyWrite) error {
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
		next.Fields[p] = pushed(next.Fields[p], before[p], h.now(), string(uid))
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

// pushed is a field's former values with the value a write replaced at
// their head. A head that equals it was left by a write that did not stand,
// and goes; a zero value is no former value.
func pushed(list []formerEntry, was memberValue, now time.Time, by string) []formerEntry {
	if len(list) > 0 && bytes.Equal(fromHistory(list[0].Value), was.raw) {
		list = list[1:]
	}
	if was.zero {
		return list
	}
	return append([]formerEntry{{Value: toHistory(was.raw), Until: now, By: by}}, list...)
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

// toHistory is a former value as the history keeps it, and fromHistory
// what it was when read back. Sealing at rest (ADR 0006, step 3) seals and
// opens here, under the record's subject's key, bound to the store, the
// record and the field: today both keep the value as it is.
func toHistory(raw json.RawMessage) json.RawMessage { return raw }

// fromHistory is a former value the history kept, as it was.
func fromHistory(raw json.RawMessage) json.RawMessage { return raw }
