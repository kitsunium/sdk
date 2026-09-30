// Package kit — reading a record's former values.
package kit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
)

// Reading what a store remembers (ADR 0007 §1, §4): a field's former values,
// a person's former values in their export, what an erasure clears of them,
// and what the model says of a store's history.

// Former is one former value of a field that keeps its history: what it was,
// until when, and who replaced it.
type Former = model.Former

// Former returns the former values of the field at pointer — its JSON
// pointer (RFC 6901), "/email" — in the record under key, newest first:
// each with when it was replaced and by whom. A secret field's former values
// say when they changed, never what they were: their Value is empty. A field
// that keeps no former values is an [Invalid] error, a missing record a
// [NotFound].
func (s *StoreService[T]) Former(ctx context.Context, key, pointer string) ([]Former, error) {
	a := s.app()
	if a == nil {
		return nil, notRunning(&s.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: model.EdgeReads, op: model.OpRead, name: "Former"})
	out, err := s.former(ctx, key, pointer)
	sp.end(err)
	return out, err
}

// former reads the former values of one field, a secret's without them.
func (s *StoreService[T]) former(ctx context.Context, key, pointer string) ([]Former, error) {
	eng := s.engine()
	if eng == nil {
		return nil, notRunning(&s.nodeBase)
	}
	h, _ := eng.(*historied[T])
	if h == nil || h.keeps[pointer] == 0 {
		return nil, Invalid(fmt.Sprintf("%s keeps no former values of %q: tag the field history=N", s.name, clip(pointer)))
	}
	all, err := h.formerAll(ctx, key)
	if err != nil {
		return nil, err
	}
	shown := s.plan().classOf(pointer) != model.ClassSecret
	out := make([]Former, 0, len(all[pointer]))
	for _, e := range all[pointer] {
		f := Former{Until: e.Until, By: e.By}
		if shown {
			f.Value = e.Value
		}
		out = append(out, f)
	}
	return out, nil
}

// historied is the store's history, nil while the store does not run or
// when it remembers nothing.
func (s *StoreService[T]) historied() *historied[T] {
	h, _ := s.engine().(*historied[T])
	return h
}

// formerAll reads the former values of every field of the record under key,
// newest first, opened: the record first, then its history, whose head is
// skipped where it is the field's value in the record read — left by a
// write that did not stand. Read in this order, the answer is the record's
// past as of the value read, never that value: a write meanwhile puts it at
// the head. A former value whose data key is destroyed is gone.
func (h *historied[T]) formerAll(ctx context.Context, key string) (map[string][]formerEntry, error) {
	v, err := h.Get(ctx, key)
	if err != nil {
		return nil, h.s.said(err, key, "")
	}
	if h.hist == nil {
		return nil, nil
	}
	doc, _, err := h.load(ctx, key, CodeHistoryRead)
	if err != nil {
		return nil, err
	}
	now := h.values(v)
	out := make(map[string][]formerEntry, len(doc.Fields))
	for p, list := range doc.Fields {
		opened, err := h.opened(ctx, key, p, list)
		if err != nil {
			return nil, err
		}
		if cur, ok := now[p]; ok && len(opened) > 0 && bytes.Equal(opened[0].Value, cur.raw) {
			opened = opened[1:]
		}
		if len(opened) > 0 {
			out[p] = opened
		}
	}
	return out, nil
}

// opened is the former values of the field at pointer, opened: those whose
// data key is destroyed left out.
func (h *historied[T]) opened(ctx context.Context, key, pointer string, list []formerEntry) ([]formerEntry, error) {
	out := make([]formerEntry, 0, len(list))
	for _, e := range list {
		v, err := h.fromHistory(ctx, key, pointer, e.Value)
		switch {
		case errors.Is(err, errErased):
			continue
		case err != nil:
			return nil, err
		}
		e.sealed = isBoxValue(e.Value)
		e.Value = v
		out = append(out, e)
	}
	return out, nil
}

// exportFormer is the former values of the record under key as its person
// receives them — a secret field's left out —, or for the Studio's preview
// with every personal, special or secret value redacted. A record deleted
// since the export read it has none.
func (s *StoreService[T]) exportFormer(ctx context.Context, key string, preview bool) (map[string][]Former, error) {
	h := s.historied()
	if h == nil || h.hist == nil {
		return nil, nil
	}
	all, err := h.formerAll(ctx, key)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	plan, out := s.plan(), map[string][]Former{}
	for _, p := range slices.Sorted(maps.Keys(all)) {
		if plan.classOf(p) == model.ClassSecret {
			continue
		}
		for _, e := range all[p] {
			f := Former{Value: e.Value, Until: e.Until, By: e.By}
			if preview {
				f.Value = plan.shownFormer(p, f.Value)
			}
			out[p] = append(out[p], f)
		}
	}
	return out, nil
}

// classOf is the class of the field at pointer, or of a field it is in,
// whichever is the stricter: "" when unclassified, and secret when the type
// no longer has it — what kit cannot judge, it never gives.
func (p *classPlan) classOf(pointer string) string {
	m := p.member(pointer)
	if m == nil {
		return model.ClassSecret
	}
	class := m.tag.effective()
	for _, parent := range p.members {
		if parent.tag.sensitive() && within(pointer, parent.pointer) {
			class = stricter(class, parent.tag.effective())
		}
	}
	return class
}

// strictness orders the classes kit hides: a secret is never given, special
// data is personal data flagged apart.
// A class it does not rank ranks 0, below personal data.
var strictness = rankOf([]string{"", model.ClassPersonal, model.ClassSpecial, model.ClassSecret})

// stricter is the stricter of two classes.
func stricter(x, y string) string {
	if strictness[y] > strictness[x] {
		return y
	}
	return x
}

// erasesSensitive reports whether an erasure clears the field at pointer: a
// personal, special or secret field, the subject, or a field inside one —
// or one the type no longer has, which kit cannot judge.
func (p *classPlan) erasesSensitive(pointer string) bool {
	if p.member(pointer) == nil {
		return true
	}
	return slices.ContainsFunc(p.members, func(m member) bool {
		return m.tag.sensitive() && within(pointer, m.pointer)
	})
}

// within reports whether pointer names the field at parent, or one inside
// it.
func within(pointer, parent string) bool {
	return pointer == parent || strings.HasPrefix(pointer, parent+"/")
}

// erasable reports whether an erasure of v would clear anything: a member
// it clears, or a former value or a version of one — a record whose members
// are already cleared may still remember what they held.
func (s *StoreService[T]) erasable(ctx context.Context, v T) bool {
	return !s.plan().cleared(reflect.ValueOf(&v).Elem()) || s.keepsErasable(ctx, s.keyOf(v)) ||
		s.versionsErasable(ctx, s.keyOf(v))
}

// keepsErasable reports whether the record under key keeps former values an
// erasure clears. A history kit cannot read is: the erasure tries, and says
// why it cannot.
func (s *StoreService[T]) keepsErasable(ctx context.Context, key string) bool {
	h := s.historied()
	if h == nil || h.hist == nil {
		return false
	}
	doc, _, err := h.load(ctx, key, CodeHistoryRead)
	if err != nil {
		return true
	}
	plan := s.plan()
	for p, list := range doc.Fields {
		if len(list) > 0 && plan.erasesSensitive(p) {
			return true
		}
	}
	return false
}

// erasing is ctx for an erasure's write: the former values of the fields it
// clears go with them.
func (s *StoreService[T]) erasing(ctx context.Context, members []*member) context.Context {
	if members == nil {
		return withIntent(ctx, historyIntent{erases: s.plan().erasesSensitive})
	}
	return withIntent(ctx, historyIntent{erases: func(pointer string) bool {
		return slices.ContainsFunc(members, func(m *member) bool { return within(pointer, m.pointer) })
	}})
}
