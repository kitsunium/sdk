// Package kit — the plan of a Go type's classified members, computed once per
// type.
package kit

import (
	"encoding"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/codec/jsonshape"
)

var (
	// plans caches every type's plan, as the redactor caches its own.
	plans sync.Map // reflect.Type → *classPlan

	// Types whose JSON form reflection cannot see: kit cannot classify inside
	// them, only the field that holds one.
	//
	// jsonMarshaler is a type that writes its own JSON.
	jsonMarshaler = reflect.TypeFor[json.Marshaler]()
	// textMarshaler is a type that writes itself as text.
	textMarshaler = reflect.TypeFor[encoding.TextMarshaler]()
	// timeTyp is an instant, which JSON writes as text.
	timeTyp = reflect.TypeFor[time.Time]()
)

// A type's classification ----------------------------------------------------

// rules is what kit knows of a Go type's classified members, for a walk
// over its values: a struct's fields that carry a kit tag or lead to one,
// or the elements of a slice, an array or a map. A recursive type shares its
// own rules, so a walk over a value follows it as deep as the value goes.
type rules struct {
	fields []fieldRule
	// byName finds a field of fields by its JSON name, for a walk over a
	// document rather than a value (seal_json.go).
	byName map[string]*fieldRule
	// elem are the rules of every element of a slice or an array, or every
	// value of a map; nil when they hold nothing classified.
	elem *rules
	// warnings are the fields of this struct, promoted ones included, whose
	// name and type read like personal data and whose tag gives no class —
	// or gives secret, a credential's.
	warnings []fieldWarning
}

// field is the field whose JSON name is name, or nil.
func (r *rules) field(name []byte) *fieldRule {
	if r == nil {
		return nil
	}
	return r.byName[string(name)]
}

// fieldRule is one struct field a value walk visits.
type fieldRule struct {
	// index reaches the field through every embedding (FieldByIndex).
	index []int
	// name is its JSON name; goName and owner name it for the messages.
	name, goName string
	owner        reflect.Type
	typ          reflect.Type
	tag          fieldTag
	// sub are the rules of the field's own type; nil when it holds nothing
	// classified.
	sub *rules
}

// step is one step from a value to one of its members: a struct field, or
// every element of a collection.
type step struct {
	index []int
	each  bool
}

// member is one classified field of a type, as a JSON pointer.
type member struct {
	// pointer is the field's JSON pointer (RFC 6901) in the value, where
	// "*" stands for every element of an array or every value of a map:
	// "/email", "/replies/*/body".
	pointer string
	path    []step
	tag     fieldTag
	typ     reflect.Type
	// field names it for a person: "Report.Email".
	field string
}

// classPlan is what kit knows of one type: its rules for value walks, its
// classified members, its subject, and what its tags get wrong.
type classPlan struct {
	typ     reflect.Type
	rules   *rules
	members []member
	// subject is the member tagged subject, when there is exactly one
	// reachable without crossing a list or a map.
	subject *member
	// problems refuse the start; warnings are said in dev, in config and in
	// the Studio, one per declared field.
	problems []phrase
	warnings []fieldWarning
}

// flattener lists a plan's members and gathers the warnings of the structs
// it meets.
type flattener struct {
	stack    map[*rules]bool
	members  []member
	warnings []fieldWarning
}

// planOf returns the plan of t, read once.
func planOf(t reflect.Type) *classPlan {
	if t == nil {
		return &classPlan{}
	}
	if cached, ok := plans.Load(t); ok {
		if p, isPlan := cached.(*classPlan); isPlan {
			return p
		}
	}
	p := buildPlan(t)
	actual, _ := plans.LoadOrStore(t, p)
	if kept, isPlan := actual.(*classPlan); isPlan {
		return kept
	}
	return p
}

// planFor is planOf the type argument.
func planFor[T any]() *classPlan { return planOf(reflect.TypeFor[T]()) }

// buildPlan reads t: its rules, then its members, and judges them.
func buildPlan(t reflect.Type) *classPlan {
	p := &classPlan{typ: t, rules: buildRules(t, map[reflect.Type]*rules{})}
	f := flattener{stack: map[*rules]bool{}}
	f.flatten(p.rules, "", nil)
	// A field met twice — in a struct used twice side by side, or promoted
	// into several — is warned of once.
	p.members, p.warnings = f.members, uniqueWarnings(f.warnings)
	subjects := p.judgeMembers()
	switch {
	case len(subjects) > 1:
		p.problems = append(p.problems, say("classify.subject-twice", "type", t, "first", subjects[0].field, "second", subjects[1].field))
	case len(subjects) == 1 && subjects[0].identity():
		p.subject = subjects[0]
	}
	return p
}

// judgeMembers says what each member's tag gets wrong, in the members'
// order, and returns the members tagged subject.
func (p *classPlan) judgeMembers() []*member {
	var subjects []*member
	for i := range p.members {
		m := &p.members[i]
		for _, problem := range m.tag.problems {
			p.problems = append(p.problems, problem(m.field))
		}
		if m.tag.marks.has(markErased) && !isTime(m.typ) {
			p.problems = append(p.problems, say("classify.erased-type", "field", m.field, "type", m.typ))
		}
		if m.tag.history > 0 && m.inCollection() {
			// An element of a list has no identity from one version to the
			// next: a field's former values are one field's (ADR 0007).
			p.problems = append(p.problems, say("classify.history-many", "field", m.field))
		}
		if m.tag.marks.has(markSubject) {
			subjects = append(subjects, m)
			p.judgeSubject(m)
		}
	}
	return subjects
}

// judgeSubject refuses a subject kit cannot read as one person's identity:
// one inside a list or a map, one that is neither a string nor an integer.
func (p *classPlan) judgeSubject(m *member) {
	switch {
	case m.inCollection():
		p.problems = append(p.problems, say("classify.subject-many", "field", m.field))
	case !subjectKind(m.typ):
		p.problems = append(p.problems, say("classify.subject-type", "field", m.field, "type", m.typ))
	}
}

// inCollection reports whether the member is reached through an element of
// a list or a map.
func (m *member) inCollection() bool {
	return slices.ContainsFunc(m.path, func(s step) bool { return s.each })
}

// identity reports whether the member can be read as one person's identity.
func (m *member) identity() bool { return !m.inCollection() && subjectKind(m.typ) }

// opaque reports whether t writes its own JSON: a time, a json.RawMessage,
// a type with a MarshalJSON.
func opaque(t reflect.Type) bool {
	for _, m := range []reflect.Type{jsonMarshaler, textMarshaler} {
		if t.Implements(m) || reflect.PointerTo(t).Implements(m) {
			return true
		}
	}
	return false
}

// isTime reports whether t is what an erased field must be: a time.Time or
// a pointer to one.
func isTime(t reflect.Type) bool {
	return t == timeTyp || (t.Kind() == reflect.Pointer && t.Elem() == timeTyp)
}

// subjectKind reports whether a subject field can be read as an identity: a
// string or an integer, through a pointer.
func subjectKind(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	default:
		// Every other kind has no rules of its own.
	}
	return false
}

// buildRules walks t the way encoding/json lays it out — the SDK's jsonshape
// resolves each struct's members, embeddings and promotions included — and
// returns its rules. seen holds the struct rules being built, so a recursive
// type shares its own.
func buildRules(t reflect.Type, seen map[reflect.Type]*rules) *rules {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if opaque(t) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		return structRules(t, seen)
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil // bytes: a string on the wire
		}
		return elemRules(t.Elem(), seen)
	case reflect.Map:
		return elemRules(t.Elem(), seen)
	default:
		// Every other kind has no rules of its own.
	}
	return nil
}

// elemRules are the rules of a collection whose elements are of type t; nil
// when they hold nothing classified.
func elemRules(t reflect.Type, seen map[reflect.Type]*rules) *rules {
	if e := buildRules(t, seen); e != nil {
		return &rules{elem: e}
	}
	return nil
}

// structRules are a struct's rules: its fields that carry a kit tag or lead
// to one. The struct's rules are shared before its fields are read, so a
// recursive type's rules hold themselves.
func structRules(t reflect.Type, seen map[reflect.Type]*rules) *rules {
	if r, ok := seen[t]; ok {
		return r
	}
	r := &rules{}
	seen[t] = r
	for _, f := range jsonshape.Of(t).Fields {
		sf := t.FieldByIndex(f.Index)
		tag := parseTag(f.Tag.Get("kit"))
		if w, ok := nameWarning(t, sf.Type, tag, f); ok {
			r.warnings = append(r.warnings, w)
		}
		sub := buildRules(sf.Type, seen)
		if tag.any() || sub != nil {
			r.fields = append(r.fields, fieldRule{index: f.Index, name: f.Name, goName: f.GoName, owner: t, typ: sf.Type, tag: tag, sub: sub})
		}
	}
	r.byName = make(map[string]*fieldRule, len(r.fields))
	for i := range r.fields {
		r.byName[r.fields[i].name] = &r.fields[i]
	}
	return r
}

// nameWarning is the warning the field f of struct t, of type ft, deserves
// by its name: one unclassified that reads like personal data, one tagged
// secret that reads so and says no credential.
func nameWarning(t, ft reflect.Type, tag fieldTag, f jsonshape.Field) (fieldWarning, bool) {
	switch class := tag.effective(); {
	case class == "" && looksPersonal(ft, f.Name, f.GoName):
		return looksPersonalWarning(t, f.Index, f.GoName), true
	case class == model.ClassSecret && secretLooksPersonal(ft, f.Name, f.GoName):
		return secretPersonalWarning(t, f.Index, f.GoName), true
	}
	return fieldWarning{}, false
}

// flatten lists the classified members under r, as JSON pointers under
// prefix, reached by path. A recursive type is listed down to its first
// recursion: its members below are those already listed above.
func (f *flattener) flatten(r *rules, prefix string, path []step) {
	if r == nil || f.stack[r] {
		return
	}
	f.stack[r] = true
	defer delete(f.stack, r)
	f.warnings = append(f.warnings, r.warnings...)
	if r.elem != nil {
		f.flatten(r.elem, prefix+"/*", append(slices.Clone(path), step{each: true}))
	}
	for _, fr := range r.fields {
		pointer := prefix + "/" + escapePointer(fr.name)
		p := append(slices.Clone(path), step{index: fr.index})
		if fr.tag.any() {
			f.members = append(f.members, member{pointer: pointer, path: p, tag: fr.tag, typ: fr.typ, field: fieldLabel(fr.owner, fr.goName)})
		}
		f.flatten(fr.sub, pointer, p)
	}
}

// fieldLabel names a field for a person: "shop.Report.Email", or the field
// alone in a struct with no name.
func fieldLabel(owner reflect.Type, field string) string {
	if owner.Name() == "" {
		return field
	}
	return owner.String() + "." + field
}

// escapePointer escapes a member name as RFC 6901 asks: "~" as "~0", "/" as
// "~1".
func escapePointer(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}

// What a plan tells ------------------------------------------------------------

// sensitive reports whether the type holds a member kit clears on erasure.
func (p *classPlan) sensitive() bool {
	return slices.ContainsFunc(p.members, func(m member) bool { return m.tag.sensitive() })
}

// personal reports whether the type holds data about a person.
func (p *classPlan) personal() bool {
	return slices.ContainsFunc(p.members, func(m member) bool { return m.tag.personal() })
}

// sealsAny reports whether the type holds a member kit seals at rest: a
// personal, special or secret one, or the subject, that is not plain.
func (p *classPlan) sealsAny() bool {
	return slices.ContainsFunc(p.members, func(m member) bool { return m.tag.sealedAtRest() })
}

// member returns the member at pointer, or nil.
func (p *classPlan) member(pointer string) *member {
	for i := range p.members {
		if p.members[i].pointer == pointer {
			return &p.members[i]
		}
	}
	return nil
}

// subjectPointer is the subject's JSON pointer, or "".
func (p *classPlan) subjectPointer() string {
	if p.subject == nil {
		return ""
	}
	return p.subject.pointer
}
