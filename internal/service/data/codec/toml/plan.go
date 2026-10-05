package toml

import (
	"encoding"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The options a toml struct tag may carry after the name, and the standalone
// boolean tags the previous library also read.
const (
	// tagName is the struct tag the codec reads.
	tagName string = "toml"
	// tagComment is the struct tag whose value is written as a comment above
	// the field.
	tagComment string = "comment"
	// tagSkip is the tag that drops a field.
	tagSkip string = "-"
	// tagTrue is the value of a standalone boolean tag.
	tagTrue string = "true"
	// optOmitEmpty drops an empty value.
	optOmitEmpty string = "omitempty"
	// optOmitZero drops a zero value.
	optOmitZero string = "omitzero"
	// optInline writes a table as an inline table.
	optInline string = "inline"
	// optMultiline writes a string with a newline as a multi-line string, and
	// an array one element per line.
	optMultiline string = "multiline"
	// optCommented writes the value, and everything under it, commented out.
	optCommented string = "commented"
)

// The roles a type plays.
const (
	// typeTextMarshaler: the type implements encoding.TextMarshaler.
	typeTextMarshaler typeFlags = 1 << iota
	// typeTextMarshalerPtr: only *T implements encoding.TextMarshaler.
	typeTextMarshalerPtr
	// typeTextUnmarshaler: *T implements encoding.TextUnmarshaler.
	typeTextUnmarshaler
	// typeZeroer: T or *T has an IsZero method, which omitzero then uses.
	typeZeroer
	// typeSpecial: the type is time.Time or one of the local types.
	typeSpecial
	// typeValue: the type encodes as a value rather than as a table.
	typeValue
	// typeBadNames: a tag names a key that is not valid UTF-8.
	typeBadNames
)

// foldBufferBytes is the longest key folded to lower case on the stack.
const foldBufferBytes int = 64

// fieldOptions are the options of one struct field.
type fieldOptions struct {
	// comment is written above the field.
	comment string
	// omitEmpty drops an empty value.
	omitEmpty bool
	// omitZero drops a zero value.
	omitZero bool
	// inline writes a table inline.
	inline bool
	// multiline writes strings and arrays across lines.
	multiline bool
	// commented writes the value commented out.
	commented bool
}

// fieldPlan is how one struct field maps to a key.
type fieldPlan struct {
	// name is the key.
	name string
	// index is the field's path through embedded structs.
	index []int
	// opts are the tag's options.
	opts fieldOptions
	// depth is how many embedded structs the field is reached through.
	depth int
}

// typeInfo is what the codec knows about one Go type.
type typeInfo struct {
	// fields are a struct's keys, in declaration order, shadowed ones removed.
	fields []fieldPlan
	// exact finds a field by its exact key.
	exact map[string]int
	// folded finds a field by its key in lower case, for a key whose case
	// matches no field exactly.
	folded map[string]int
	// flags are the roles the type plays.
	flags typeFlags
}

// typeFlags are the roles a type plays, one bit each.
type typeFlags uint8

// The reflect types the codec treats specially.
var (
	// timeType is time.Time.
	timeType = reflect.TypeFor[time.Time]()
	// localDateType is LocalDate.
	localDateType = reflect.TypeFor[LocalDate]()
	// localTimeType is LocalTime.
	localTimeType = reflect.TypeFor[LocalTime]()
	// localDateTimeType is LocalDateTime.
	localDateTimeType = reflect.TypeFor[LocalDateTime]()
	// textMarshalerType is encoding.TextMarshaler.
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	// textUnmarshalerType is encoding.TextUnmarshaler.
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	// isZeroerType is the method omitzero consults when a type has it:
	// IsZero reports whether the value is its type's zero.
	isZeroerType = reflect.TypeFor[interface{ IsZero() bool }]()
	// typeInfos caches a *typeInfo per reflect.Type.
	typeInfos sync.Map
	// predeclaredInfo describes every predeclared scalar type — bool, the
	// integers, the floats, string — which can have no method, and so plays
	// no role but being a value.
	predeclaredInfo = &typeInfo{flags: typeValue}
)

// infoOf returns the cached typeInfo of t, computing it on first use.
func infoOf(t reflect.Type) *typeInfo {
	//: a predeclared scalar type needs no lookup: an empty package path on a
	//: scalar kind can only be one, and it has no methods.
	if t.PkgPath() == "" && t.Kind() >= reflect.Bool && t.Kind() <= reflect.Float64 || t.PkgPath() == "" && t.Kind() == reflect.String {
		//: the shared description.
		return predeclaredInfo
	}
	//: a type is described once per process.
	if cached, ok := typeInfos.Load(t); ok {
		//: the description, which only this function stores.
		if info, isInfo := cached.(*typeInfo); isInfo {
			//: found.
			return info
		}
	}
	info := describe(t)
	actual, _ := typeInfos.LoadOrStore(t, info)
	//: the first description stored wins a race; both are identical.
	if stored, isInfo := actual.(*typeInfo); isInfo {
		//: the stored one.
		return stored
	}
	//: unreachable: only *typeInfo values are stored.
	return info
}

// describe computes the typeInfo of t.
func describe(t reflect.Type) *typeInfo {
	info := &typeInfo{flags: methodFlags(t)}
	kindOfT := t.Kind()
	//: a value is anything but a map or a struct, and a struct that writes itself.
	if info.flags&(typeSpecial|typeTextMarshaler|typeTextMarshalerPtr) != 0 || (kindOfT != reflect.Map && kindOfT != reflect.Struct) {
		info.flags |= typeValue
	}
	//: only a struct has fields.
	if kindOfT == reflect.Struct {
		info.fields = structFields(t)
		info.exact, info.folded = fieldIndexes(info.fields)
		//: a tag that is not UTF-8 cannot be written as a key.
		if slices.ContainsFunc(info.fields, func(f fieldPlan) bool { return !utf8.ValidString(f.name) }) {
			info.flags |= typeBadNames
		}
	}
	//: the description.
	return info
}

// methodFlags returns the roles t plays through the methods it, or its
// pointer, has, and whether it is one of the types written specially.
func methodFlags(t reflect.Type) typeFlags {
	ptr := reflect.PointerTo(t)
	textMarshaler := t.Implements(textMarshalerType)
	flags := flagIf(textMarshaler, typeTextMarshaler) |
		flagIf(!textMarshaler && ptr.Implements(textMarshalerType), typeTextMarshalerPtr) |
		flagIf(ptr.Implements(textUnmarshalerType), typeTextUnmarshaler) |
		flagIf(ptr.Implements(isZeroerType), typeZeroer)
	//: time.Time and the three local types.
	if t == timeType || t == localDateType || t == localTimeType || t == localDateTimeType {
		flags |= typeSpecial
	}
	//: the roles.
	return flags
}

// flagIf returns flag when cond holds, and no flag otherwise.
func flagIf(cond bool, flag typeFlags) typeFlags {
	//: the condition decides.
	if cond {
		//: set.
		return flag
	}
	//: unset.
	return 0
}

// is reports whether the type plays every role in flags.
func (ti *typeInfo) is(flags typeFlags) bool {
	//: all of them.
	return ti.flags&flags == flags
}

// structFields returns the fields of struct t in declaration order,
// flattening embedded structs as encoding/json does. Of two fields with one
// key, the shallower wins, and at equal depth the first declared.
func structFields(t reflect.Type) []fieldPlan {
	var fields []fieldPlan
	collectFields(&fields, t, nil, 0, map[reflect.Type]bool{})
	byName := make(map[string]int, len(fields))
	kept := fields[:0]
	//: one field per key.
	for _, f := range fields {
		at, seen := byName[f.name]
		//: the first field with this key.
		if !seen {
			byName[f.name] = len(kept)
			kept = append(kept, f)
			continue
		}
		//: a shallower field shadows the one kept.
		if f.depth < kept[at].depth {
			kept[at] = f
		}
	}
	//: the visible fields.
	return kept
}

// collectFields appends the fields of t, reached through index, to fields.
// visited stops a struct that embeds itself through a pointer.
func collectFields(fields *[]fieldPlan, t reflect.Type, index []int, depth int, visited map[reflect.Type]bool) {
	//: a type embedded in itself adds nothing the first visit did not.
	if visited[t] {
		return
	}
	visited[t] = true
	defer delete(visited, t)
	//: each field, in declaration order.
	for i := range t.NumField() {
		f := t.Field(i)
		tag, tagged := f.Tag.Lookup(tagName)
		//: "-" drops the field.
		if tag == tagSkip {
			continue
		}
		path := append(append(make([]int, 0, len(index)+1), index...), i)
		name, opts := parseTag(tag)
		//: an untagged embedded struct is flattened into its parent.
		if embedded := embeddedStruct(f); embedded != nil && name == "" {
			collectFields(fields, embedded, path, depth+1, visited)
			continue
		}
		//: an unexported field is not the codec's to read or write.
		if !f.IsExported() {
			continue
		}
		//: an untagged field is keyed by its Go name.
		if !tagged || name == "" {
			name = f.Name
		}
		opts = standaloneTags(f.Tag, opts)
		*fields = append(*fields, fieldPlan{name: name, index: path, opts: opts, depth: depth})
	}
}

// embeddedStruct returns the struct type an anonymous field embeds, directly
// or through a pointer, or nil.
func embeddedStruct(f reflect.StructField) reflect.Type {
	//: a named field embeds nothing.
	if !f.Anonymous {
		//: not embedded.
		return nil
	}
	t := f.Type
	//: an embedded *T flattens T.
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	//: only a struct has fields to flatten.
	if t.Kind() != reflect.Struct {
		//: an embedded non-struct is a field like any other.
		return nil
	}
	//: the struct to flatten.
	return t
}

// parseTag splits a toml tag into its name and options.
func parseTag(tag string) (name string, opts fieldOptions) {
	name, rest, _ := strings.Cut(tag, ",")
	//: each option after the name.
	for rest != "" {
		var opt string
		opt, rest, _ = strings.Cut(rest, ",")
		applyOption(&opts, opt)
	}
	//: the name and the options.
	return name, opts
}

// applyOption records one tag option; an unknown one is ignored, as the
// previous library ignored it.
func applyOption(opts *fieldOptions, opt string) {
	switch opt {
	//: omitempty.
	case optOmitEmpty:
		opts.omitEmpty = true
	//: omitzero.
	case optOmitZero:
		opts.omitZero = true
	//: inline.
	case optInline:
		opts.inline = true
	//: multiline.
	case optMultiline:
		opts.multiline = true
	//: commented.
	case optCommented:
		opts.commented = true
	//: anything else.
	default:
	}
}

// standaloneTags applies the standalone multiline:"true", inline:"true" and
// commented:"true" tags and the comment tag.
func standaloneTags(tag reflect.StructTag, opts fieldOptions) fieldOptions {
	opts.multiline = opts.multiline || tag.Get(optMultiline) == tagTrue
	opts.inline = opts.inline || tag.Get(optInline) == tagTrue
	opts.commented = opts.commented || tag.Get(optCommented) == tagTrue
	opts.comment = tag.Get(tagComment)
	//: the options.
	return opts
}

// fieldIndexes builds the exact and the case-folded key indexes of fields.
// The first field to claim a folded key keeps it.
func fieldIndexes(fields []fieldPlan) (exact, folded map[string]int) {
	exact = make(map[string]int, len(fields))
	folded = make(map[string]int, len(fields))
	//: each field.
	for i, f := range fields {
		exact[f.name] = i
		lower := strings.ToLower(f.name)
		//: the first field keeps a folded key two fields share.
		if _, taken := folded[lower]; !taken {
			folded[lower] = i
		}
	}
	//: both indexes.
	return exact, folded
}

// lookup returns the field a key names: by exact match first, then
// case-insensitively, as encoding/json and the previous library match.
func (ti *typeInfo) lookup(key []byte) (*fieldPlan, bool) {
	i, ok := ti.exact[string(key)]
	//: the key in lower case.
	if !ok {
		i, ok = ti.lookupFolded(key)
	}
	//: no field has this key: it is ignored.
	if !ok {
		//: unknown.
		return nil, false
	}
	//: found.
	return &ti.fields[i], true
}

// lookupFolded finds a field by its key in lower case. An ASCII key short
// enough is folded on the stack, so the lookup allocates nothing.
func (ti *typeInfo) lookupFolded(key []byte) (int, bool) {
	var buf [foldBufferBytes]byte
	//: a long or non-ASCII key takes Unicode's lower case.
	if len(key) > len(buf) || !isASCII(key) {
		i, ok := ti.folded[strings.ToLower(string(key))]
		//: found or not.
		return i, ok
	}
	//: each byte, upper-case ASCII letters folded.
	for i, c := range key {
		//: A-Z.
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		buf[i] = c
	}
	i, ok := ti.folded[string(buf[:len(key)])]
	//: found or not.
	return i, ok
}

// isASCII reports whether b is ASCII.
func isASCII(b []byte) bool {
	//: each byte.
	for _, c := range b {
		//: a non-ASCII byte.
		if c >= charFirstNonASCII {
			//: not ASCII.
			return false
		}
	}
	//: ASCII.
	return true
}
