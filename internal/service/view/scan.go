// Package view — the trust-type scan that runs BEFORE the template does.
package view

import (
	"html/template"
	"reflect"
	"strconv"
	"sync"
)

// rootPathSegment names the render model in a reported path, so a violation
// reads data.Items[3].Body rather than starting mid-sentence at .Items.
const rootPathSegment string = "data"

// startDetectingCyclesAfter is the descent depth past which the scan starts
// recording the pointers it has followed.
//
// The threshold and the technique are encoding/json's, deliberately: a model
// that is genuinely 1000 levels deep is not a model, and paying for a map on
// every render to catch a shape nobody writes would tax every render for the
// benefit of none. Below the threshold a cycle simply recurses, exactly as
// json.Marshal does.
const startDetectingCyclesAfter int = 1000

// decimalBase is strconv's base for an integer map key rendered into a path.
const decimalBase int = 10

// unsafeTypeNames maps each REFUSED html/template trust type to the name the
// error reports.
//
// html/template has seven trust types and this is six of them. The seventh,
// template.HTML, is the one the domain admits — through core/view.TrustHTML,
// which is its only SDK spelling.
//
// The six are refused because each disables contextual escaping for a context
// where a plain string could not have done any harm, and each has a working
// exploit that the same string unconverted does not: measured,
// template.URL("javascript:alert(1)") reaches the browser as
// href="javascript:alert%281%29" and executes, while the identical string
// typed as a string is rewritten by html/template to the inert "#ZgotmplZ".
var (
	// unsafeTypeNames is the refused-type table described above.
	unsafeTypeNames = map[reflect.Type]string{
		reflect.TypeFor[template.CSS]():      "html/template.CSS",
		reflect.TypeFor[template.HTMLAttr](): "html/template.HTMLAttr",
		reflect.TypeFor[template.JS]():       "html/template.JS",
		reflect.TypeFor[template.JSStr]():    "html/template.JSStr",
		reflect.TypeFor[template.Srcset]():   "html/template.Srcset",
		reflect.TypeFor[template.URL]():      "html/template.URL",
	}

	// reachCache memoises canReachUnsafe per reflect.Type.
	//
	// sync.Map is the right shape for the same reason validation's plan cache
	// uses it: the key set is bounded by the program's own types, it stops
	// growing within the first requests, and every access after that is a
	// read. A copy-on-write snapshot would clone the whole map per newly-seen
	// type.
	reachCache sync.Map
)

// canReachUnsafe reports whether a value of typ could TRANSITIVELY hold one of
// the six refused trust types.
//
// This is the gate that decides whether the scan costs anything at all. A
// render model made of strings, numbers, times and structs of those cannot
// hold a template.URL no matter what its fields contain, so the answer is a
// property of the TYPE and is computed once per type for the life of the
// process. Only an interface-typed field (including any and map[string]any)
// forces a value-level walk, because only there is the dynamic type unknown
// until the render.
func canReachUnsafe(typ reflect.Type) bool {
	//: a nil type is what an untyped nil model reports; nothing to reach.
	if typ == nil {
		//: no type, no members, no trust type.
		return false
	}
	//: memoised: the answer never changes for a given type.
	if cached, found := reachCache.Load(typ); found {
		//: comma-ok even though this map is written in exactly one place: an
		//: unchecked assertion here would turn a future typo into a panic on
		//: the render path.
		if answer, isBool := cached.(bool); isBool {
			//: the cached verdict.
			return answer
		}
	}
	result := reachable(typ, map[reflect.Type]bool{})
	reachCache.Store(typ, result)
	//: hand back the freshly computed verdict.
	return result
}

// reachable is canReachUnsafe's recursion. visiting breaks type-level cycles.
//
// An in-progress type answers false rather than true, and that is sound rather
// than optimistic: re-entering a type already on the stack introduces no
// member the outer frame is not already examining, so the cycle contributes
// nothing the outer answer will not already carry.
func reachable(typ reflect.Type, visiting map[reflect.Type]bool) bool {
	//: the six refused types are the recursion's base case.
	if _, refused := unsafeTypeNames[typ]; refused {
		//: found one statically — no value walk needed to know.
		return true
	}
	//: a type already on the stack adds nothing the outer frame misses.
	if visiting[typ] {
		//: break the type-level cycle without poisoning the answer.
		return false
	}
	visiting[typ] = true
	answer := reachableKind(typ, visiting)
	delete(visiting, typ)
	//: hand the composed answer back to the parent frame.
	return answer
}

// reachableKind answers reachable for the composite kinds.
func reachableKind(typ reflect.Type, visiting map[reflect.Type]bool) bool {
	//: the composite kinds are the only ones with members to inspect.
	switch typ.Kind() {
	//: an interface hides its dynamic type until render time.
	case reflect.Interface:
		//: the dynamic type is unknown until the render; assume the worst.
		return true
	//: a single-element container.
	case reflect.Pointer, reflect.Slice, reflect.Array:
		//: a container reaches exactly what its element reaches.
		return reachable(typ.Elem(), visiting)
	//: a map has two member types.
	case reflect.Map:
		//: a map key can be a trust type too — map[template.URL]string.
		return reachable(typ.Key(), visiting) || reachable(typ.Elem(), visiting)
	//: a struct reaches whatever any of its fields reaches.
	case reflect.Struct:
		//: unexported fields are included: they cost nothing extra here and
		//: leaving them out would make the gate narrower than the walk.
		return reachableStruct(typ, visiting)
	//: every remaining kind is a leaf.
	default:
		//: a leaf that is not one of the six reaches nothing.
		return false
	}
}

// reachableStruct reports whether any field of typ can reach a refused type.
func reachableStruct(typ reflect.Type, visiting map[reflect.Type]bool) bool {
	//: every field, exported or not — a gate narrower than the walk is a hole.
	for field := range typ.Fields() {
		//: one reachable field is enough to make the whole struct suspect.
		if reachable(field.Type, visiting) {
			//: stop at the first — the gate is a yes/no, not a census.
			return true
		}
	}
	//: no field reaches one; a value of this type can be skipped entirely.
	return false
}

// scanner carries the per-render walk state. It lives on the stack and its map
// is allocated lazily, so a scan that never passes
// [startDetectingCyclesAfter] allocates nothing.
type scanner struct {
	// seen records the pointers followed past the cycle-detection threshold.
	seen map[visitedKey]struct{}
}

// scanUnsafe reports the first refused trust type reachable in data, with the
// path that leads to it.
//
// The path is built out of GO field names — data.User.Avatar, not a json tag —
// because the vocabulary a template author reads is {{.User.Avatar}} and the
// vocabulary a developer greps for is the struct field. That is the opposite
// choice from validation (ADR 0046), whose paths name json members because its
// input arrived as a decoded document.
func scanUnsafe(data any) (path, typeName string, found bool) {
	//: an untyped nil model has nothing to scan, and the gate eliminates most
	//: of the rest without touching a value — canReachUnsafe answers false for
	//: a nil type, so the two questions share one guard.
	if data == nil || !canReachUnsafe(reflect.TypeOf(data)) {
		//: nothing found, nothing to report.
		return "", "", false
	}
	state := scanner{}
	suffix, name, hit := state.walk(reflect.ValueOf(data), 0)
	//: no hit — the walk confirmed what the gate could not rule out.
	if !hit {
		//: nothing found, nothing to report.
		return "", "", false
	}
	//: prefix the model's own name so the path reads as a whole sentence.
	return rootPathSegment + suffix, name, true
}

// walk descends value, returning the path SUFFIX of the first refused type.
//
// The suffix is assembled on the way back up rather than threaded down, so a
// scan that finds nothing — every scan on a healthy request — builds no string
// at all.
//
// The order of the four checks is measured rather than stylistic. Type lookups
// were 45 % of this walk's CPU before the two short-circuits below existed: an
// interface's own type is never one of the six and can always hold one, so
// BOTH lookups are known answers; and the six refused types are all defined
// STRING types, so no other kind can ever be in the table.
func (s *scanner) walk(value reflect.Value, depth int) (suffix, typeName string, found bool) {
	//: an invalid Value is a nil interface element; nothing below it.
	if !value.IsValid() {
		//: nothing found, nothing to report.
		return "", "", false
	}
	kind := value.Kind()
	//: an interface answers both type questions before they are asked.
	if kind == reflect.Interface {
		//: re-gate on the DYNAMIC type one frame down.
		return s.walkIndirect(value, depth, false)
	}
	//: only a string kind can BE one of the six.
	if kind == reflect.String {
		//: the refused types are checked before the gate: they ARE the answer.
		if name, refused := unsafeTypeNames[value.Type()]; refused {
			//: the hit itself contributes no path segment.
			return "", name, true
		}
		//: a string that is not one of the six holds nothing below it.
		return "", "", false
	}
	//: two prunes, one guard: a leaf kind holds nothing (and asking the
	//: reachability cache would cost a hash and an atomic load to learn what
	//: the kind already said), and a composite whose static type cannot hold
	//: one of the six holds nothing either.
	if !composite(kind) || !canReachUnsafe(value.Type()) {
		//: nothing found, nothing to report.
		return "", "", false
	}
	//: the kinds that can carry something below them.
	return s.walkKind(value, kind, depth)
}

// composite reports whether kind can hold another value inside it.
func composite(kind reflect.Kind) bool {
	//: one switch over the five descents the walk knows how to make.
	switch kind {
	//: the five composite kinds.
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map, reflect.Struct:
		//: these five are the only descents the walk makes.
		return true
	//: everything else.
	default:
		//: every other kind is a leaf.
		return false
	}
}

// walkKind dispatches walk over the composite kinds. The interface case is
// handled in walk itself, before either type lookup.
func (s *scanner) walkKind(value reflect.Value, kind reflect.Kind, depth int) (suffix, typeName string, found bool) {
	//: one switch over the five descents composite() admitted.
	switch kind {
	//: a pointer.
	case reflect.Pointer:
		//: a pointer is where a value-level cycle can close.
		return s.walkIndirect(value, depth, true)
	//: an indexed container.
	case reflect.Slice, reflect.Array:
		//: indexed containers report [i] segments.
		return s.walkIndexed(value, depth)
	//: a map.
	case reflect.Map:
		//: both halves of an entry are scanned; either can be a trust type.
		return s.walkMap(value, depth)
	//: a struct.
	case reflect.Struct:
		//: struct fields report .Name segments.
		return s.walkStruct(value, depth)
	//: unreachable by construction.
	default:
		//: composite() admitted only the five cases above.
		return "", "", false
	}
}

// walkIndirect follows an interface or a pointer to the value beneath it.
// cycles is true for the pointer case, which is the only one that can close a
// loop back onto a value already scanned.
func (s *scanner) walkIndirect(value reflect.Value, depth int, cycles bool) (suffix, typeName string, found bool) {
	//: two ways to have nothing below: a nil interface or nil pointer, and a
	//: pointer already followed — which leads to a subtree already scanned, so
	//: revisiting it would loop forever and add nothing.
	if value.IsNil() || (cycles && s.repeat(value, depth, 0)) {
		//: nothing found, nothing to report.
		return "", "", false
	}
	//: the element carries no path segment of its own.
	return s.walk(value.Elem(), depth+1)
}

// walkIndexed scans a slice or array, reporting [i] path segments.
func (s *scanner) walkIndexed(value reflect.Value, depth int) (suffix, typeName string, found bool) {
	//: a slice can close a cycle; its length distinguishes two slices that
	//: share one backing array, exactly as encoding/json's key does.
	if value.Kind() == reflect.Slice && s.repeat(value, depth, value.Len()) {
		//: this backing array has already been scanned at this length.
		return "", "", false
	}
	//: every element, in order, so the reported index is the real one.
	for index := range value.Len() {
		below, name, hit := s.walk(value.Index(index), depth+1)
		//: the first hit ends the scan; the report names one location.
		if hit {
			//: prepend this element's index on the way back up.
			return "[" + strconv.Itoa(index) + "]" + below, name, true
		}
	}
	//: no element reached one.
	return "", "", false
}

// walkStruct scans every field, reporting .Name path segments.
//
// Unexported fields are scanned. They cannot be read by a template — text
// /template refuses them — but scanning them costs nothing the gate has not
// already priced in, and a scan narrower than its own gate would be a hole
// that is hard to see.
func (s *scanner) walkStruct(value reflect.Value, depth int) (suffix, typeName string, found bool) {
	//: every field, exported or not — a walk narrower than its gate is a hole.
	for field, member := range value.Fields() {
		//: prune per field: most fields of a suspect struct are not suspect.
		if !canReachUnsafe(field.Type) {
			continue
		}
		below, name, hit := s.walk(member, depth+1)
		//: the first hit ends the scan; the report names one location.
		if hit {
			//: prepend this field's Go name on the way back up.
			return "." + field.Name + below, name, true
		}
	}
	//: no field reached one.
	return "", "", false
}

// walkMap scans every key and every value, reporting [key] path segments.
//
// The iteration reuses ONE addressable key and ONE addressable value through
// Value.SetIterKey/SetIterValue rather than taking iter.Key()/iter.Value().
// Measured: the convenient spelling allocates twice PER ENTRY — reflect boxes
// each key and each element into a fresh reflect.Value — and it was 97 % of
// every allocation this scan made.
func (s *scanner) walkMap(value reflect.Value, depth int) (suffix, typeName string, found bool) {
	//: a map can close a cycle just as a pointer can.
	if s.repeat(value, depth, 0) {
		//: this map has already been scanned in this walk.
		return "", "", false
	}
	mapType := value.Type()
	key := reflect.New(mapType.Key()).Elem()
	entry := reflect.New(mapType.Elem()).Elem()
	iter := value.MapRange()
	//: every entry; both halves of each are scanned.
	for iter.Next() {
		key.SetIterKey(iter)
		entry.SetIterValue(iter)
		label := mapKeyLabel(key)
		below, name, hit := s.walk(key, depth+1)
		//: a key that IS a trust type is reported at the key position.
		if hit {
			//: (key) disambiguates the half of the entry that offended.
			return "[" + label + "](key)" + below, name, true
		}
		below, name, hit = s.walk(entry, depth+1)
		//: the first hit ends the scan; the report names one location.
		if hit {
			//: prepend the entry's key on the way back up.
			return "[" + label + "]" + below, name, true
		}
	}
	//: no entry reached one.
	return "", "", false
}

// mapKeyLabel renders a map key for a path segment.
//
// It never renders a key it cannot spell exactly. A path is a Field on a typed
// error, Fields are log-side, and a map key is frequently caller data — so an
// unrenderable key becomes "?" rather than a reflected dump of whatever it was.
func mapKeyLabel(key reflect.Value) string {
	//: one switch over the key kinds that have an exact decimal spelling.
	switch key.Kind() {
	//: a string key.
	case reflect.String:
		//: the common case: a string key is its own label.
		return key.String()
	//: a signed integer key.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		//: an integer key renders exactly, with no formatting verb.
		return strconv.FormatInt(key.Int(), decimalBase)
	//: an unsigned integer key.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		//: same, unsigned.
		return strconv.FormatUint(key.Uint(), decimalBase)
	//: anything else.
	default:
		//: not spelled rather than guessed at.
		return "?"
	}
}
