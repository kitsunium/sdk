package cbor

import (
	"cmp"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// The struct tags a field's key and options are read from, and the options.
const (
	// cborTagKey is the struct tag read first.
	cborTagKey string = "cbor"
	// jsonTagKey is the struct tag read when a field has no cbor tag.
	jsonTagKey string = "json"
	// skipTagName excludes a field from the wire.
	skipTagName string = "-"
	// optionOmitEmpty omits a field whose value encodes as an empty CBOR item.
	optionOmitEmpty string = "omitempty"
	// optionOmitZero omits a field holding its type's zero value.
	optionOmitZero string = "omitzero"
	// optionKeyAsInt keys a field by the integer its name spells.
	optionKeyAsInt string = "keyasint"
	// optionToArray, on the blank field `_`, encodes the struct as an array.
	optionToArray string = "toarray"
	// structOptionsField is the blank field whose cbor tag carries the
	// struct-level options.
	structOptionsField string = "_"
)

// fieldFlag is one boolean property of a struct field.
type fieldFlag uint8

// The properties a struct field can have.
const (
	// flagKeyAsInt keys the field by its integer name.
	flagKeyAsInt fieldFlag = 1 << iota
	// flagTagged is set when a tag named or configured the field, which makes
	// it dominate an untagged field of the same name at the same depth.
	flagTagged
	// flagOmitEmpty is the omitempty option.
	flagOmitEmpty
	// flagOmitZero is the omitzero option.
	flagOmitZero
)

// structField is one field a struct puts on the wire.
type structField struct {
	// name is the key: the tag's name or the Go name, or for a keyasint field
	// the canonical decimal form of its integer.
	name string
	// index is the path from the struct to the field, through embedded ones.
	index []int
	// typ is the field's Go type.
	typ reflect.Type
	// nameInt is the key of a keyasint field.
	nameInt int64
	// flags are the field's properties.
	flags fieldFlag
}

// structLayout is the wire shape of one struct type.
type structLayout struct {
	// fields are the fields on the wire, in declaration order.
	fields []*structField
	// refusal, when not empty, says why no value of the type can be encoded
	// or decoded: a keyasint field whose name is not an integer.
	refusal string
	// toArray is the toarray option: the struct is a CBOR array.
	toArray bool
}

// embedded is the set of paths at which one embedded struct type appears at
// the level being resolved.
type embedded map[reflect.Type][][]int

// has reports whether the field carries flag.
func (f *structField) has(flag fieldFlag) bool {
	//: a bit test.
	return f.flags&flag != 0
}

// layoutOf resolves the wire shape of the struct type t.
func layoutOf(t reflect.Type) structLayout {
	fields, refusal := canonicalIntKeys(collectFields(t))
	fields = dominantFields(fields)
	slices.SortFunc(fields, compareIndex)
	//: declaration order, embedded fields where their struct is declared.
	return structLayout{fields: fields, refusal: refusal, toArray: hasToArray(t)}
}

// collectFields gathers every candidate field of t, level by level through
// embedded structs. An embedded type is descended once, at the shallowest
// level it appears; two of the same type at one level cancel each other.
func collectFields(t reflect.Type) []*structField {
	fields, next := appendFields(t, nil, nil, nil)
	visited := map[reflect.Type]bool{t: true}
	//: one level of embedding per round.
	for len(next) > 0 {
		current := next
		next = nil
		//: each embedded type found at this level.
		for typ, paths := range current {
			//: ambiguous at this level, or already resolved shallower.
			if len(paths) > 1 || visited[typ] {
				//: contributes nothing.
				continue
			}
			visited[typ] = true
			fields, next = appendFields(typ, paths[0], fields, next)
		}
	}
	//: every candidate, duplicates included.
	return fields
}

// appendFields appends the candidate fields declared by t, reached at path
// prefix, and records its embedded structs in next for the following level.
func appendFields(t reflect.Type, prefix []int, fields []*structField, next embedded) ([]*structField, embedded) {
	//: every declared field, in order.
	for i := range t.NumField() {
		f := t.Field(i)
		tag := fieldTag(f)
		//: unexported and not an embedded struct, or excluded by its tag.
		if !onWire(f) || tag == skipTagName {
			//: not on the wire.
			continue
		}
		name, options, _ := strings.Cut(tag, ",")
		path := append(slices.Clone(prefix), i)
		//: an untagged embedded struct lends its fields instead.
		if name == "" && f.Anonymous && derefType(f.Type).Kind() == reflect.Struct {
			next = recordEmbedded(next, derefType(f.Type), path)
			//: descended at the next level.
			continue
		}
		//: an unexported embedded struct only ever lends its fields.
		if f.IsExported() {
			fields = append(fields, newStructField(f, name, tag, options, path))
		}
	}
	//: this level's fields, and the next level's types.
	return fields, next
}

// recordEmbedded adds path to the paths at which typ is embedded.
func recordEmbedded(next embedded, typ reflect.Type, path []int) embedded {
	//: created on the first embedded struct.
	if next == nil {
		next = embedded{}
	}
	next[typ] = append(next[typ], path)
	//: recorded.
	return next
}

// onWire reports whether f can carry a field: an exported field, or an
// embedded struct (or pointer to one) whose exported fields are promoted.
func onWire(f reflect.StructField) bool {
	//: exported, or an embedded struct whatever its name.
	return f.IsExported() || (f.Anonymous && derefType(f.Type).Kind() == reflect.Struct)
}

// fieldTag returns the field's cbor tag, or its json tag when it has none.
func fieldTag(f reflect.StructField) string {
	//: the cbor tag wins when present.
	if tag := f.Tag.Get(cborTagKey); tag != "" {
		//: read as written.
		return tag
	}
	//: the json tag otherwise.
	return f.Tag.Get(jsonTagKey)
}

// newStructField builds the candidate for the Go field f, keyed by name (or
// its Go name when the tag names none) with the comma-separated options.
func newStructField(f reflect.StructField, name, tag, options string, path []int) *structField {
	field := &structField{name: cmp.Or(name, f.Name), index: path, typ: f.Type}
	//: a tag — even one carrying options alone — makes the field dominant.
	if tag != "" {
		field.flags |= flagTagged
	}
	//: the options after the name.
	for option := range strings.SplitSeq(options, ",") {
		field.flags |= optionFlag(option)
	}
	//: one candidate.
	return field
}

// optionFlag maps a tag option to the flag it sets, or 0 for an option this
// codec does not know, which is ignored as fxamacker/cbor ignored it.
func optionFlag(option string) fieldFlag {
	//: the three per-field options.
	switch option {
	case optionOmitEmpty:
		//: omitempty.
		return flagOmitEmpty
	case optionOmitZero:
		//: omitzero, honoured in a json tag too, as encoding/json does.
		return flagOmitZero
	case optionKeyAsInt:
		//: keyasint.
		return flagKeyAsInt
	default:
		//: ignored.
		return 0
	}
}

// derefType strips every pointer level from t.
func derefType(t reflect.Type) reflect.Type {
	//: *T, **T and so on all embed T.
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	//: the pointed-to type.
	return t
}

// canonicalIntKeys parses the name of every keyasint field into its integer
// and rewrites the name in canonical decimal, so "01", "+1" and "1" are one
// key. A name that is not an integer refuses the whole type.
func canonicalIntKeys(fields []*structField) ([]*structField, string) {
	refusal := ""
	//: only keyasint fields are rewritten.
	for _, f := range fields {
		//: a text key stays as written.
		if !f.has(flagKeyAsInt) {
			continue
		}
		n, err := strconv.Atoi(f.name)
		//: the first unparsable name is the refusal.
		if err != nil {
			refusal = cmp.Or(refusal, "a keyasint field whose name is not an integer")
			continue
		}
		f.nameInt, f.name = int64(n), strconv.Itoa(n)
	}
	//: the fields, and why the type is refused if it is.
	return fields, refusal
}

// dominantFields keeps, for each key, the one field that wins it: the
// shallowest, and at equal depth the only tagged one. A key no field wins
// outright is dropped, as encoding/json drops it.
func dominantFields(fields []*structField) []*structField {
	slices.SortStableFunc(fields, compareDominance)
	kept := fields[:0]
	//: one run of fields per key.
	for start := 0; start < len(fields); {
		end := start + 1
		//: the run sharing this key.
		for end < len(fields) && sameKey(fields[start], fields[end]) {
			end++
		}
		//: the first of the run wins only if it beats the second.
		if end == start+1 || dominates(fields[start], fields[start+1]) {
			kept = append(kept, fields[start])
		}
		start = end
	}
	//: one field per key.
	return kept
}

// compareDominance orders candidates by key, then integer keys first, then
// shallowest first, then tagged first.
func compareDominance(a, b *structField) int {
	//: the key first.
	if c := strings.Compare(a.name, b.name); c != 0 {
		//: different keys.
		return c
	}
	//: an integer key and a text key are different keys.
	if a.has(flagKeyAsInt) != b.has(flagKeyAsInt) {
		//: integer keys first.
		return boolOrder(a.has(flagKeyAsInt)) - boolOrder(b.has(flagKeyAsInt))
	}
	//: shallower first.
	if len(a.index) != len(b.index) {
		//: by depth.
		return len(a.index) - len(b.index)
	}
	//: tagged first; otherwise equal, and the stable sort keeps their order.
	return boolOrder(a.has(flagTagged)) - boolOrder(b.has(flagTagged))
}

// boolOrder sorts true before false: −1 for true, 0 for false.
func boolOrder(first bool) int {
	//: true sorts first.
	if first {
		//: before.
		return -1
	}
	//: after.
	return 0
}

// sameKey reports whether a and b compete for one key.
func sameKey(a, b *structField) bool {
	//: same name in the same namespace.
	return a.name == b.name && a.has(flagKeyAsInt) == b.has(flagKeyAsInt)
}

// dominates reports whether a, sorted before b, wins the key outright.
func dominates(a, b *structField) bool {
	//: shallower, or as deep and the only tagged one.
	return len(a.index) < len(b.index) || (a.has(flagTagged) && !b.has(flagTagged))
}

// compareIndex orders fields by their path, which is declaration order with
// an embedded struct's fields where the struct is declared.
func compareIndex(a, b *structField) int {
	//: lexicographic over the paths, a prefix first.
	return slices.Compare(a.index, b.index)
}

// hasToArray reports whether t's blank field `_` carries the toarray option.
func hasToArray(t reflect.Type) bool {
	f, ok := t.FieldByName(structOptionsField)
	//: no blank field, no struct options.
	if !ok {
		//: a map.
		return false
	}
	_, options, _ := strings.Cut(f.Tag.Get(cborTagKey), ",")
	//: the option is a whole word of the list.
	for option := range strings.SplitSeq(options, ",") {
		//: found.
		if option == optionToArray {
			//: an array.
			return true
		}
	}
	//: a map.
	return false
}
