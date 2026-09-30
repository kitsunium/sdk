// Package kit — a document as it lies at rest, sealed member by member.
package kit

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
)

// A document as it lies at rest (ADR 0006 §4). Sealing works on the JSON a
// store keeps and a queue carries, member by member, and never decodes what
// it does not touch: an object's members are read in their order, and the
// document is written again in the same order, only the values that change
// replaced. What it reads was written by encoding/json — the document store
// and the queues keep what it wrote —, and a document it cannot read is left
// as it is: decoding it says what is wrong.

// membersCap is the room an object's members are first read into.
const membersCap int = 8

// boxMark is how a sealed member's value starts in a document: a JSON
// string whose text starts with boxPrefix (seal.go).
var boxMark = []byte(`"` + boxPrefix)

// hasBoxes reports whether a document may hold a sealed member.
func hasBoxes(doc []byte) bool { return bytes.Contains(doc, boxMark) }

// jsonMember is one member of a JSON object: its name as written — the
// quoted string —, its name read — the same bytes when it holds no escape
// —, and its value's bytes.
type jsonMember struct {
	rawName []byte
	name    []byte
	value   []byte
}

// skipSpace is the first index at or after i that is not JSON whitespace.
func skipSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

// scanString is the index after the JSON string that starts at b[i], a
// quote.
func scanString(b []byte, i int) (int, bool) {
	for j := i + 1; j < len(b); j++ {
		switch b[j] {
		case '\\':
			j++
		case '"':
			return j + 1, true
		}
	}
	return len(b), false
}

// scanValue is the index after the JSON value that starts at b[i].
func scanValue(b []byte, i int) (int, bool) {
	if i >= len(b) {
		return i, false
	}
	switch b[i] {
	case '"':
		return scanString(b, i)
	case '{', '[':
		return scanNested(b, i)
	}
	j := i
	for j < len(b) && !strings.ContainsRune(",}] \t\r\n", rune(b[j])) {
		j++
	}
	return j, j > i
}

// scanNested is the index after the JSON object or array that starts at
// b[i].
func scanNested(b []byte, i int) (int, bool) {
	depth := 0
	for j := i; j < len(b); j++ {
		switch b[j] {
		case '"':
			end, ok := scanString(b, j)
			if !ok {
				return len(b), false
			}
			j = end - 1
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return j + 1, true
			}
		}
	}
	return len(b), false
}

// openedJSON is the index after the opener that starts the JSON value raw, and
// whether the value closes at once; ok is false when raw does not start
// with opener.
func openedJSON(raw []byte, opener, closer byte) (i int, empty, ok bool) {
	i = skipSpace(raw, 0)
	if i >= len(raw) || raw[i] != opener {
		return i, false, false
	}
	i = skipSpace(raw, i+1)
	return i, i < len(raw) && raw[i] == closer, true
}

// afterItem reads what follows an item of an object or an array that ends
// at raw[end]: a comma, and next is the next item's index, or the closer,
// and done is true; ok is false for anything else.
func afterItem(raw []byte, end int, closer byte) (next int, done, ok bool) {
	i := skipSpace(raw, end)
	switch {
	case i < len(raw) && raw[i] == ',':
		return skipSpace(raw, i+1), false, true
	case i < len(raw) && raw[i] == closer:
		return i, true, true
	}
	return i, false, false
}

// unquote reads a JSON string's text: the bytes between its quotes when it
// holds no escape, a copy decoded otherwise.
func unquote(raw []byte) ([]byte, bool) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return nil, false
	}
	if bytes.IndexByte(raw, '\\') < 0 {
		return raw[1 : len(raw)-1], true
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return nil, false
	}
	return []byte(s), true
}

// objectMembers reads the members of the JSON object raw, in order; ok is
// false when raw is not an object.
func objectMembers(raw []byte) (members []jsonMember, ok bool) {
	i, empty, ok := openedJSON(raw, '{', '}')
	switch {
	case !ok:
		return nil, false
	case empty:
		return []jsonMember{}, true
	}
	members = make([]jsonMember, 0, membersCap)
	for {
		m, end, ok := readMember(raw, i)
		if !ok {
			return nil, false
		}
		members = append(members, m)
		next, done, ok := afterItem(raw, end, '}')
		switch {
		case !ok:
			return nil, false
		case done:
			return members, true
		}
		i = next
	}
}

// readMember reads the member of an object that starts at raw[i] — its name,
// a colon, its value — and returns the index after it.
func readMember(raw []byte, i int) (m jsonMember, end int, ok bool) {
	if i >= len(raw) || raw[i] != '"' {
		return m, i, false
	}
	nameEnd, ok := scanString(raw, i)
	if !ok {
		return m, i, false
	}
	m.rawName = raw[i:nameEnd]
	if m.name, ok = unquote(m.rawName); !ok {
		return m, i, false
	}
	i = skipSpace(raw, nameEnd)
	if i >= len(raw) || raw[i] != ':' {
		return m, i, false
	}
	i = skipSpace(raw, i+1)
	end, ok = scanValue(raw, i)
	if !ok {
		return m, i, false
	}
	m.value = raw[i:end]
	return m, end, true
}

// arrayElems reads the elements of the JSON array raw, in order; ok is false
// when raw is not an array.
func arrayElems(raw []byte) (elems [][]byte, ok bool) {
	i, empty, ok := openedJSON(raw, '[', ']')
	switch {
	case !ok:
		return nil, false
	case empty:
		return [][]byte{}, true
	}
	for {
		end, ok := scanValue(raw, i)
		if !ok {
			return nil, false
		}
		elems = append(elems, raw[i:end])
		next, done, ok := afterItem(raw, end, ']')
		switch {
		case !ok:
			return nil, false
		case done:
			return elems, true
		}
		i = next
	}
}

// writeObject writes members as a JSON object, in their order.
func writeObject(members []jsonMember) []byte {
	n := 2 + len(members)*2
	for _, m := range members {
		n += len(m.rawName) + len(m.value)
	}
	b := make([]byte, 0, n)
	b = append(b, '{')
	for i, m := range members {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(append(append(b, m.rawName...), ':'), m.value...)
	}
	return append(b, '}')
}

// writeArray writes elems as a JSON array, in their order.
func writeArray(elems [][]byte) []byte {
	return append(append([]byte{'['}, bytes.Join(elems, []byte{','})...), ']')
}

// isNull reports whether a JSON value is null.
func isNull(v []byte) bool { return bytes.Equal(bytes.TrimSpace(v), []byte("null")) }

// isBoxValue reports whether a JSON value is a sealed member's: a string
// that starts as a box does.
func isBoxValue(v []byte) bool { return bytes.HasPrefix(v, boxMark) }

// memberAt is where a walk reached a member: its JSON pointer (RFC 6901)
// in the document, its rule — nil where no rule classifies it —, and
// whether it lies inside a list or a map.
type memberAt struct {
	pointer string
	f       *fieldRule
	inList  bool
}

// memberVisit is what a walk does with one member it reaches: it returns
// the value to keep, or drop to leave the member out.
type memberVisit func(at memberAt, value []byte) (out []byte, drop bool, err error)

// sealWalk calls visit on every member of doc that r says kit seals at rest,
// as deep as r and doc go, and returns doc with what visit returned. A
// member inside a list or a map is reached with its index or its key in its
// pointer: "/notes/0/body".
func sealWalk(doc []byte, r *rules, pointer string, inList bool, visit memberVisit) ([]byte, bool, error) {
	if r == nil || isNull(doc) {
		return doc, false, nil
	}
	if r.elem != nil {
		return eachChild(doc, pointer, func(p string, v []byte) ([]byte, bool, error) {
			return sealWalk(v, r.elem, p, true, visit)
		})
	}
	members, ok := objectMembers(doc)
	if !ok {
		return doc, false, nil
	}
	kept, changed, err := sealMembers(members, r, pointer, inList, visit)
	if err != nil || !changed {
		return doc, false, err
	}
	return writeObject(kept), true, nil
}

// sealMembers is sealWalk over the members of an object r rules: the members
// kept, with what visit returned, and whether any changed.
func sealMembers(members []jsonMember, r *rules, pointer string, inList bool, visit memberVisit) ([]jsonMember, bool, error) {
	changed, kept := false, members[:0:0]
	for _, m := range members {
		out, drop, err := sealMember(m, r.field(m.name), pointer, inList, visit)
		if err != nil {
			return nil, false, err
		}
		if drop {
			changed = true
			continue
		}
		changed = changed || !bytes.Equal(out, m.value)
		m.value = out
		kept = append(kept, m)
	}
	return kept, changed, nil
}

// sealMember is sealWalk's step on the member m, whose rule is f: its value
// visited when kit seals it, walked when f has rules below, kept otherwise.
func sealMember(m jsonMember, f *fieldRule, pointer string, inList bool, visit memberVisit) ([]byte, bool, error) {
	if f == nil {
		return m.value, false, nil
	}
	p := memberPointer(pointer, m.name)
	switch {
	case f.tag.sealedAtRest():
		return visit(memberAt{pointer: p, f: f, inList: inList}, m.value)
	case f.sub != nil:
		out, ch, err := sealWalk(m.value, f.sub, p, inList, visit)
		if err != nil || !ch {
			return m.value, false, err
		}
		return out, false, nil
	}
	return m.value, false, nil
}

// eachChild calls fn on every element of the JSON array doc, or every value
// of the JSON object doc — a map's —, with its pointer, and returns doc with
// what fn returned.
func eachChild(doc []byte, pointer string, fn childVisit) ([]byte, bool, error) {
	if elems, ok := arrayElems(doc); ok {
		return eachJSONElem(doc, elems, pointer, fn)
	}
	members, ok := objectMembers(doc)
	if !ok {
		return doc, false, nil
	}
	changed := false
	for i, m := range members {
		out, ch, err := fn(memberPointer(pointer, m.name), m.value)
		if err != nil {
			return doc, false, err
		}
		if ch {
			members[i].value, changed = out, true
		}
	}
	if !changed {
		return doc, false, nil
	}
	return writeObject(members), true, nil
}

// memberPointer is the JSON pointer of the member named name of the object
// at pointer.
func memberPointer(pointer string, name []byte) string {
	return pointer + "/" + escapePointer(string(name))
}

// childVisit is what eachChild does with one child: the value to keep, and
// whether it changed.
type childVisit func(pointer string, v []byte) ([]byte, bool, error)

// eachJSONElem is eachChild over the elements of an array.
func eachJSONElem(doc []byte, elems [][]byte, pointer string, fn childVisit) ([]byte, bool, error) {
	changed := false
	for i, e := range elems {
		out, ch, err := fn(pointer+"/"+strconv.Itoa(i), e)
		if err != nil {
			return doc, false, err
		}
		if ch {
			elems[i], changed = out, true
		}
	}
	if !changed {
		return doc, false, nil
	}
	return writeArray(elems), true, nil
}

// boxWalk calls visit on every sealed member of doc — every string value
// that is a box, wherever it lies, classified or not: a field whose class
// changed since it was sealed still opens —, with the rule r gives it, nil
// where none does. A subtree with no box is not read.
func boxWalk(doc []byte, r *rules, pointer string, inList bool, visit memberVisit) ([]byte, bool, error) {
	if !hasBoxes(doc) {
		return doc, false, nil
	}
	var elemRules *rules
	if r != nil {
		elemRules = r.elem
	}
	if elems, ok := arrayElems(doc); ok {
		return boxElems(doc, elems, elemRules, pointer, visit)
	}
	return boxMembers(doc, boxScope{rules: r, elemRules: elemRules, pointer: pointer, inList: inList}, visit)
}

// boxElems is boxWalk over the elements of an array: a dropped one is null.
func boxElems(doc []byte, elems [][]byte, elemRules *rules, pointer string, visit memberVisit) ([]byte, bool, error) {
	changed := false
	for i, e := range elems {
		if !hasBoxes(e) {
			continue
		}
		p := pointer + "/" + strconv.Itoa(i)
		out, ch, err := boxChild(e, elemRules, memberAt{pointer: p, inList: true}, visit)
		if err != nil {
			return doc, false, err
		}
		if ch {
			if out == nil {
				out = []byte("null")
			}
			elems[i], changed = out, true
		}
	}
	if !changed {
		return doc, false, nil
	}
	return writeArray(elems), true, nil
}

// boxScope is where boxWalk reached an object: the rules of its members —
// a struct's —, or of its values — a map's —, its pointer, and whether it
// lies inside a list or a map.
type boxScope struct {
	rules     *rules
	elemRules *rules
	pointer   string
	inList    bool
}

// member is where the walk reaches the member named name, and the rules
// below it.
func (b boxScope) member(name []byte) (memberAt, *rules) {
	at := memberAt{pointer: memberPointer(b.pointer, name), inList: b.inList}
	if b.elemRules != nil {
		// A map's values.
		at.inList = true
		return at, b.elemRules
	}
	at.f = b.rules.field(name)
	if at.f == nil {
		return at, nil
	}
	return at, at.f.sub
}

// boxMembers is boxWalk over the members of an object — a struct's, or a
// map's —: a dropped one is left out.
func boxMembers(doc []byte, b boxScope, visit memberVisit) ([]byte, bool, error) {
	members, ok := objectMembers(doc)
	if !ok {
		return doc, false, nil
	}
	changed, kept := false, members[:0:0]
	for _, m := range members {
		if !hasBoxes(m.value) {
			// Nothing sealed below: kept as it is, its pointer never made.
			kept = append(kept, m)
			continue
		}
		at, sub := b.member(m.name)
		out, ch, err := boxChild(m.value, sub, at, visit)
		if err != nil {
			return doc, false, err
		}
		switch {
		case ch && out == nil:
			changed = true
			continue
		case ch:
			m.value, changed = out, true
		}
		kept = append(kept, m)
	}
	if !changed {
		return doc, false, nil
	}
	return writeObject(kept), true, nil
}

// boxChild is boxWalk's step on one value: a box is visited, an object or an
// array walked; a nil out with changed drops the value.
func boxChild(v []byte, sub *rules, at memberAt, visit memberVisit) ([]byte, bool, error) {
	if isBoxValue(v) {
		out, drop, err := visit(at, v)
		if err != nil || drop {
			return nil, drop, err
		}
		return out, !bytes.Equal(out, v), nil
	}
	if len(v) > 0 && (v[0] == '{' || v[0] == '[') {
		return boxWalk(v, sub, at.pointer, at.inList, visit)
	}
	return v, false, nil
}

// setElem is setSegs on the list elems, at the element name names.
func setElem(elems [][]byte, name string, rest []string, value []byte) ([]byte, bool) {
	i, ok := elemIndex(elems, name)
	if !ok {
		return nil, false
	}
	if elems[i], ok = setIn(elems[i], rest, value); !ok {
		return nil, false
	}
	return writeArray(elems), true
}

// setIn is v with value along rest: value itself when rest is empty.
func setIn(v []byte, rest []string, value []byte) ([]byte, bool) {
	if len(rest) == 0 {
		return value, true
	}
	return setSegs(v, rest, value)
}

// zeroJSON reports whether a member's JSON is its type's zero value: what an
// erasure leaves in a field it clears.
func zeroJSON(v []byte, t reflect.Type) bool {
	switch string(bytes.TrimSpace(v)) {
	case "null", `""`, "0", "false", "[]", "{}":
		return true
	}
	if t == nil {
		return false
	}
	p := reflect.New(t)
	return json.Unmarshal(v, p.Interface()) == nil && p.Elem().IsZero()
}

// setAt sets the value at pointer in doc — a member added to its object when
// the object lacks it — and returns doc. A pointer that leads nowhere in doc
// leaves it as it is.
func setAt(doc []byte, pointer string, value []byte) []byte {
	if pointer == "" {
		return value
	}
	segs := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	for i, s := range segs {
		segs[i] = strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
	}
	out, ok := setSegs(doc, segs, value)
	if !ok {
		return doc
	}
	return out
}

// setSegs is setAt along the unescaped segments of a pointer.
func setSegs(doc []byte, segs []string, value []byte) ([]byte, bool) {
	name, rest := segs[0], segs[1:]
	if elems, ok := arrayElems(doc); ok {
		return setElem(elems, name, rest, value)
	}
	members, ok := objectMembers(doc)
	if !ok {
		return nil, false
	}
	if i := memberIndex(members, name); i >= 0 {
		out, ok := setIn(members[i].value, rest, value)
		if !ok {
			return nil, false
		}
		members[i].value = out
		return writeObject(members), true
	}
	if len(rest) > 0 {
		return nil, false
	}
	quoted, err := json.Marshal(name)
	if err != nil {
		return nil, false
	}
	return writeObject(append(members, jsonMember{rawName: quoted, name: []byte(name), value: value})), true
}
