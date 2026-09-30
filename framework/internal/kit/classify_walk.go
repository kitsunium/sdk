// Package kit — the walk of a value along its classification plan.
package kit

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/kitsunium/sdk/framework/model"
)

// Walks over values ------------------------------------------------------------

// indirect follows pointers and interfaces down to a value, without
// allocating: a nil one is the invalid Value.
func indirect(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

// subjectOf reads the subject of v, a value of the plan's type, as written:
// "" when the type has none or the value leaves it empty.
func (p *classPlan) subjectOf(v reflect.Value) (string, bool) {
	if p.subject == nil {
		return "", false
	}
	return identityOf(fieldAt(v, p.subject.path))
}

// fieldAt follows a path of struct fields down from v: the invalid Value
// when a nil pointer, a nil embedded one included, stops it.
func fieldAt(v reflect.Value, path []step) reflect.Value {
	for _, s := range path {
		v = indirect(v)
		if !v.IsValid() || v.Kind() != reflect.Struct {
			return reflect.Value{}
		}
		var err error
		if v, err = v.FieldByIndexErr(s.index); err != nil {
			return reflect.Value{}
		}
	}
	return indirect(v)
}

// identityOf writes an identity as the product stores it: a string, or an
// integer in decimal; "" for anything else.
func identityOf(v reflect.Value) (string, bool) {
	if !v.IsValid() {
		return "", false
	}
	switch v.Kind() {
	case reflect.String:
		return v.String(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10), true
	default:
		// Every other kind holds nothing to walk into.
	}
	return "", false
}

// visitor is called on each field a walk meets — the settable field itself
// — and says whether the walk goes below it.
type visitor func(f *fieldRule, fv reflect.Value) (below bool)

// visitFields calls fn on every field r names under v, and walks below the
// fields for which fn returns true.
func visitFields(v reflect.Value, r *rules, fn visitor) {
	if r == nil {
		return
	}
	v = indirect(v)
	if !v.IsValid() {
		return
	}
	if r.elem != nil {
		eachElem(v, func(e reflect.Value) { visitFields(e, r.elem, fn) })
		return
	}
	if v.Kind() != reflect.Struct {
		return
	}
	for i := range r.fields {
		f := &r.fields[i]
		fv, err := v.FieldByIndexErr(f.index)
		if err != nil {
			continue // a nil embedded pointer: nothing there
		}
		if fn(f, fv) {
			visitFields(fv, f.sub, fn)
		}
	}
}

// eachElem calls fn on every element of a slice or an array, and on every
// value of a map: a map's values are not addressable, so each is copied,
// handed to fn, and put back.
func eachElem(v reflect.Value, fn func(reflect.Value)) {
	switch v.Kind() {
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			fn(v.Index(i))
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			cp := reflect.New(v.Type().Elem()).Elem()
			cp.Set(it.Value())
			fn(cp)
			v.SetMapIndex(it.Key(), cp)
		}
	default:
		// Every other kind holds nothing to walk into.
	}
}

// clearSensitive sets every personal, special and secret member of v, and
// its subject, to its zero value. v must be addressable.
func (p *classPlan) clearSensitive(v reflect.Value) {
	visitFields(v, p.rules, func(f *fieldRule, fv reflect.Value) bool {
		if f.tag.sensitive() {
			if fv.CanSet() {
				fv.SetZero()
			}
			return false
		}
		return true
	})
}

// cleared reports whether every personal, special and secret member of v,
// and its subject, is already its zero value: an erased record, or one
// that never held anything to erase.
func (p *classPlan) cleared(v reflect.Value) bool {
	clean := true
	visitFields(v, p.rules, func(f *fieldRule, fv reflect.Value) bool {
		if f.tag.sensitive() {
			clean = clean && fv.IsZero()
			return false
		}
		return clean
	})
	return clean
}

// stampErased sets every field tagged erased to at. v must be addressable.
func (p *classPlan) stampErased(v reflect.Value, at time.Time) {
	visitFields(v, p.rules, func(f *fieldRule, fv reflect.Value) bool {
		if f.tag.marks.has(markErased) && fv.CanSet() {
			stampTime(fv, at)
		}
		return !f.tag.sensitive()
	})
}

// stampTime sets a time.Time or a *time.Time to at.
func stampTime(fv reflect.Value, at time.Time) {
	switch {
	case fv.Type() == timeTyp:
		fv.Set(reflect.ValueOf(at))
	case fv.Type().Kind() == reflect.Pointer && fv.Type().Elem() == timeTyp:
		fv.Set(reflect.ValueOf(&at))
	}
}

// clearPath sets the member at path to its zero value in v, in every
// element a "*" of its pointer stands for. v must be addressable.
func clearPath(v reflect.Value, path []step) {
	for len(path) > 0 {
		v = indirect(v)
		if !v.IsValid() {
			return
		}
		s := path[0]
		path = path[1:]
		if s.each {
			eachElem(v, func(e reflect.Value) { clearPath(e, path) })
			return
		}
		if v.Kind() != reflect.Struct {
			return
		}
		fv, err := v.FieldByIndexErr(s.index)
		if err != nil {
			return
		}
		if len(path) == 0 && fv.CanSet() {
			fv.SetZero()
		}
		v = fv
	}
}

// withoutSecrets returns doc, a JSON document of the plan's type, with its
// secret members left out, as deep as the document goes: a recursive
// type's, below its first recursion, too. Numbers are kept as written.
func (p *classPlan) withoutSecrets(doc []byte) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	dropSecrets(v, p.rules)
	return json.Marshal(v)
}

// dropSecrets removes from v, a decoded JSON value, every member r says is
// secret, following r as deep as v goes.
func dropSecrets[V any](v V, r *rules) {
	if r == nil {
		return
	}
	if r.elem != nil {
		for _, e := range jsonElems(v) {
			dropSecrets(e, r.elem)
		}
		return
	}
	obj, ok := any(v).(map[string]any)
	if !ok {
		return
	}
	for _, f := range r.fields {
		if f.tag.effective() == model.ClassSecret {
			delete(obj, f.name)
			continue
		}
		dropSecrets(obj[f.name], f.sub)
	}
}

// jsonElems are the elements of a decoded JSON array, or the values of an
// object.
func jsonElems(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case map[string]any:
		return slices.Collect(maps.Values(x))
	}
	return nil
}

// The declarations that use a type -------------------------------------------

// typeSource is a building block whose values kit shows or keeps: a store's
// entity, an endpoint's request and response, a topic's message, an auth
// handler's credentials and data, a command's and a query's input and result
// — a queued command's input kept in its queue.
type typeSource interface {
	dataTypes() []reflect.Type
}

// dataTypes are the Go types the Store carries, for the classification of
// personal data.
func (s *StoreService[T]) dataTypes() []reflect.Type { return []reflect.Type{reflect.TypeFor[T]()} }

// dataTypes are the Go types the Topic carries, for the classification of
// personal data.
func (t *TopicService[T]) dataTypes() []reflect.Type { return []reflect.Type{reflect.TypeFor[T]()} }

// dataTypes are the Go types the Endpoint carries, for the classification of
// personal data.
func (e *EndpointService[Req, Resp]) dataTypes() []reflect.Type {
	return []reflect.Type{reflect.TypeFor[Req](), reflect.TypeFor[Resp]()}
}

// dataTypes are the Go types the Authenticator carries, for the classification
// of personal data.
func (h *AuthenticatorHandler[P, D]) dataTypes() []reflect.Type {
	return []reflect.Type{reflect.TypeFor[P](), reflect.TypeFor[D]()}
}

// dataTypes are the Go types the Command carries, for the classification of
// personal data.
func (c *Command[C, R]) dataTypes() []reflect.Type {
	return []reflect.Type{reflect.TypeFor[C](), reflect.TypeFor[R]()}
}

// dataTypes are the Go types the Query carries, for the classification of
// personal data.
func (q *Query[Q, R]) dataTypes() []reflect.Type {
	return []reflect.Type{reflect.TypeFor[Q](), reflect.TypeFor[R]()}
}

// classificationProblems judges the kit tags of every type the app's
// declarations show or keep, each type once, at the first declaration that
// uses it: what a tag gets wrong is an error the start refuses. With
// explain, a field whose name and type read like personal data and which
// has no class — or the class secret, a credential's — is a warning, said
// once per declared field — at the first declaration that reaches it,
// however many types it is promoted into.
func (a *App) classificationProblems(explain bool) []model.Diagnostic {
	j := typeJudge{a: a, seenType: map[reflect.Type]bool{}, seenWarning: map[declaredField]bool{}}
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if tn, ok := n.(typeSource); ok {
				j.judge(n.base(), tn.dataTypes(), explain && !kitOwn(svc))
			}
		}
	}
	return j.out
}

// typeJudge says what the types of the app's declarations get wrong: each
// type once, each declared field's warning once.
type typeJudge struct {
	a           *App
	seenType    map[reflect.Type]bool
	seenWarning map[declaredField]bool
	out         []model.Diagnostic
}

// judge says what the types a declaration uses get wrong, at the
// declaration: their errors, and with warn their warnings.
func (j *typeJudge) judge(b *nodeBase, types []reflect.Type, warn bool) {
	for _, t := range types {
		if t == nil || j.seenType[t] {
			continue
		}
		j.seenType[t] = true
		p := planOf(t)
		for _, problem := range p.problems {
			j.out = append(j.out, diagnosticOf("error", b.id, j.a.source(&b.decl), problem))
		}
		if warn {
			j.warn(b, p.warnings)
		}
	}
}

// warn says the warning of each field not warned of yet, at the
// declaration.
func (j *typeJudge) warn(b *nodeBase, warnings []fieldWarning) {
	for _, w := range warnings {
		if !j.seenWarning[w.field] {
			j.seenWarning[w.field] = true
			j.out = append(j.out, diagnosticOf("warning", b.id, j.a.source(&b.decl), w.said))
		}
	}
}

// redacted is one of the store's entities as the Studio may show it: read
// as its type, so that its kit tags redact — personal, special and secret
// members, and the subject — as well as the names the redactor knows. A
// member sealed at rest is shown sealed: the Studio never opens it (ADR
// 0006 §9).
func (s *StoreService[T]) redacted(raw json.RawMessage) json.RawMessage {
	var sealed []string
	if hasBoxes(raw) {
		walked, _, err := boxWalk(raw, nil, "", false, func(at memberAt, _ []byte) ([]byte, bool, error) {
			sealed = append(sealed, at.pointer)
			return nil, true, nil
		})
		if err != nil {
			return nil
		}
		raw = walked
	}
	var v T
	var shown json.RawMessage
	if err := json.Unmarshal(raw, &v); err != nil {
		shown, _ = redactJSON(raw, 64<<10)
	} else {
		shown, _ = redactValue(v, 64<<10)
	}
	for _, p := range sealed {
		shown = setAt(shown, p, sealedShown)
	}
	return shown
}
