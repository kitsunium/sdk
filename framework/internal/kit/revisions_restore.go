// Package kit — a record's version written back.
package kit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/jsonshape"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// restore writes version number of the record under key back, whole or the
// fields named, through the store's own update: a new version, refused by a
// unique index it breaks as any write.
func (s *StoreService[T]) restore(ctx context.Context, key string, number uint64, fields []string) (T, error) {
	var zero T
	if _, err := s.keeper(); err != nil {
		return zero, err
	}
	kept := s.keptOnRestore()
	if err := s.restorable(fields, kept); err != nil {
		return zero, err
	}
	ver, err := s.openedVersion(ctx, key, number)
	if err != nil {
		return zero, err
	}
	return s.modify(ctx, key, func(cur *T) error {
		now, err := json.Marshal(*cur)
		if err != nil {
			return failure(CodeStoreEncode, "STORE_ENCODE", "the entity cannot be stored", err, errs.String("store", s.id))
		}
		next := slices.Clone([]byte(ver.JSON))
		if len(fields) > 0 {
			next = now
			for _, p := range fields {
				next = copyMember(next, ver.JSON, p)
			}
		}
		for _, p := range kept {
			next = copyMember(next, now, p)
		}
		var v T
		if err := json.Unmarshal(next, &v); err != nil {
			return s.versionDecodeFailure(key, number, err)
		}
		*cur = v
		return nil
	})
}

// keptOnRestore are the members a restore keeps as the record holds them:
// every secret member — a collection that holds one, whole — and the state
// of every workflow over the store.
func (s *StoreService[T]) keptOnRestore() []string {
	var out []string
	for _, m := range s.plan().members {
		if m.tag.effective() != model.ClassSecret {
			continue
		}
		p := m.pointer
		if i := strings.Index(p, "/*"); i >= 0 {
			p = p[:i]
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	for _, state := range s.states {
		if name := state(); name != "" && !slices.Contains(out, "/"+escapePointer(name)) {
			out = append(out, "/"+escapePointer(name))
		}
	}
	return out
}

// restorable refuses the fields a restore names that it cannot restore: a
// pointer that names no member of the entity, and one that lies in what the
// restore keeps — a secret member, a workflow's state.
func (s *StoreService[T]) restorable(fields, kept []string) error {
	t := reflect.TypeFor[T]()
	for _, p := range fields {
		if !strings.HasPrefix(p, "/") || !typeHas(t, splitPointer(p)) {
			return Invalid(fmt.Sprintf("%s: %q names no field of the record: name one by its JSON pointer, \"/title\"", s.name, clip(p)))
		}
		for _, k := range kept {
			if within(p, k) {
				return Invalid(fmt.Sprintf("%s: %q is a secret member or a workflow's state, which a restore keeps as it is", s.name, clip(p)))
			}
		}
	}
	return nil
}

// typeHas reports whether a value of type t can hold a member at the
// unescaped segments of a JSON pointer: a struct's field by its JSON name, as
// encoding/json lays it out, an element of a list by its index, a map's value
// by any key.
func typeHas(t reflect.Type, segs []string) bool {
	for _, seg := range segs {
		next, ok := typeAt(t, seg)
		if !ok {
			return false
		}
		t = next
	}
	return true
}

// typeAt is the type of what a value of type t holds at one unescaped
// segment of a JSON pointer, and whether it can hold anything there.
func typeAt(t reflect.Type, seg string) (reflect.Type, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		return fieldType(t, seg)
	case reflect.Slice, reflect.Array:
		if n, err := strconv.Atoi(seg); err != nil || n < 0 {
			return nil, false
		}
		return t.Elem(), true
	case reflect.Map:
		return t.Elem(), true
	default:
		return nil, false
	}
}

// fieldType is the type of the field of struct t whose JSON name is name, as
// encoding/json lays it out.
func fieldType(t reflect.Type, name string) (reflect.Type, bool) {
	if opaque(t) {
		return nil, false
	}
	for _, f := range jsonshape.Of(t).Fields {
		if f.Name == name {
			return t.FieldByIndex(f.Index).Type, true
		}
	}
	return nil, false
}

// splitPointer is a JSON pointer's segments, unescaped (RFC 6901).
func splitPointer(pointer string) []string {
	if pointer == "" {
		return nil
	}
	segs := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	for i, s := range segs {
		segs[i] = strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
	}
	return segs
}

// copyMember is doc with the member at pointer as src holds it: set — the
// objects on its way made when doc lacks them —, or left out when src has
// none there.
func copyMember(doc, src []byte, pointer string) []byte {
	if v, ok := jsonAt(src, splitPointer(pointer)); ok {
		return placeAt(doc, splitPointer(pointer), v)
	}
	return removeAt(doc, splitPointer(pointer))
}

// jsonAt is the value at the unescaped segments of a pointer in doc, and
// whether there is one.
func jsonAt(doc []byte, segs []string) ([]byte, bool) {
	for _, seg := range segs {
		next, ok := jsonValueAt(doc, seg)
		if !ok {
			return nil, false
		}
		doc = next
	}
	return doc, true
}

// jsonValueAt is the value doc holds at one unescaped segment of a pointer: an
// element of a list, a member of an object.
func jsonValueAt(doc []byte, seg string) ([]byte, bool) {
	if elems, ok := arrayElems(doc); ok {
		i, ok := elemIndex(elems, seg)
		if !ok {
			return nil, false
		}
		return elems[i], true
	}
	members, _ := objectMembers(doc)
	if i := memberIndex(members, seg); i >= 0 {
		return members[i].value, true
	}
	return nil, false
}

// elemIndex is the index seg names in elems, and whether elems has it.
func elemIndex(elems [][]byte, seg string) (int, bool) {
	i, err := strconv.Atoi(seg)
	if err != nil || i < 0 || i >= len(elems) {
		return 0, false
	}
	return i, true
}

// memberIndex is the index of the member named name, -1 for none.
func memberIndex(members []jsonMember, name string) int {
	want := []byte(name)
	return slices.IndexFunc(members, func(m jsonMember) bool { return bytes.Equal(m.name, want) })
}

// placeAt is doc with value at the unescaped segments of a pointer: a member
// missing on the way, or null, is made an object; an element of a list must
// be there. A pointer doc cannot hold leaves it as it is.
func placeAt(doc []byte, segs []string, value []byte) []byte {
	if len(segs) == 0 {
		return value
	}
	if isNull(doc) {
		doc = []byte("{}")
	}
	seg, rest := segs[0], segs[1:]
	if elems, ok := arrayElems(doc); ok {
		i, err := strconv.Atoi(seg)
		if err != nil || i < 0 || i >= len(elems) {
			return doc
		}
		elems[i] = placeAt(elems[i], rest, value)
		return writeArray(elems)
	}
	members, ok := objectMembers(doc)
	if !ok {
		return doc
	}
	if i := memberIndex(members, seg); i >= 0 {
		members[i].value = placeAt(members[i].value, rest, value)
		return writeObject(members)
	}
	quoted, err := json.Marshal(seg)
	if err != nil {
		return doc
	}
	return writeObject(append(members, jsonMember{rawName: quoted, name: []byte(seg), value: placeAt([]byte("{}"), rest, value)}))
}

// removeAt is doc without the value at the unescaped segments of a pointer:
// a member left out, an element taken out of its list.
func removeAt(doc []byte, segs []string) []byte {
	if len(segs) == 0 {
		return doc
	}
	seg, rest := segs[0], segs[1:]
	if elems, ok := arrayElems(doc); ok {
		return removeElem(doc, elems, seg, rest)
	}
	members, _ := objectMembers(doc)
	i := memberIndex(members, seg)
	switch {
	case i < 0:
		return doc
	case len(rest) == 0:
		return writeObject(slices.Delete(members, i, i+1))
	}
	members[i].value = removeAt(members[i].value, rest)
	return writeObject(members)
}

// removeElem is doc, the list elems, without the value at seg then rest.
func removeElem(doc []byte, elems [][]byte, seg string, rest []string) []byte {
	i, ok := elemIndex(elems, seg)
	switch {
	case !ok:
		return doc
	case len(rest) == 0:
		return writeArray(slices.Delete(elems, i, i+1))
	}
	elems[i] = removeAt(elems[i], rest)
	return writeArray(elems)
}
