package yaml

import (
	"reflect"
	"slices"
	"strings"
	"sync"
)

// The untyped mapping's type, and the cache: reflection runs once per struct
// type for the life of the process.
var (
	// stringMapType is map[string]any, the untyped mapping.
	stringMapType = reflect.TypeFor[map[string]any]()
	// structCache maps a reflect.Type to its *structInfo.
	structCache sync.Map
)

// fieldInfo is one key a struct reads and writes.
type fieldInfo struct {
	// key is the mapping key, as written in the document.
	key string
	// index is the path of field indices from the struct to the field,
	// through inlined structs.
	index []int
	// omitEmpty leaves the field out of the output when it is empty.
	omitEmpty bool
	// flow writes the field's collection in flow style.
	flow bool
}

// structInfo is everything the codec reads from a struct type's tags.
type structInfo struct {
	// byKey finds a field by its key.
	byKey map[string]int
	// tagError names the malformed tag, when one is; the type is then refused
	// in both directions.
	tagError string
	// fields are the struct's keys, in declaration order, inlined fields in
	// place.
	fields []fieldInfo
	// inlineMap is the index path of the map collecting the keys no field
	// declares, or nil.
	inlineMap []int
}

// tagFlags are the options after the name in a yaml tag.
type tagFlags struct {
	// omitEmpty is ",omitempty".
	omitEmpty bool
	// flow is ",flow".
	flow bool
	// inline is ",inline".
	inline bool
}

// structInfoOf returns the struct information of t, from the cache.
func structInfoOf(t reflect.Type) *structInfo {
	//: known.
	if cached, ok := structCache.Load(t); ok {
		//: cached; a value of another type would be a cache of something else.
		if info, isInfo := cached.(*structInfo); isInfo {
			return info
		}
	}
	info := &structInfo{byKey: map[string]int{}}
	buildStructInfo(info, t, nil, map[reflect.Type]bool{t: true})
	structCache.Store(t, info)
	//: computed.
	return info
}

// buildStructInfo adds t's fields to info, prefixing their index paths with
// prefix. visiting holds the structs being inlined, so a cycle of inlined
// structs is refused instead of recursing forever.
func buildStructInfo(info *structInfo, t reflect.Type, prefix []int, visiting map[reflect.Type]bool) {
	//: every field, in order.
	for i := range t.NumField() {
		//: a malformed tag stops the walk.
		if info.tagError != "" {
			return
		}
		field := t.Field(i)
		tag, ok := yamlTag(field)
		//: unexported, or "-".
		if !ok {
			continue
		}
		name, flags, _ := strings.Cut(tag, ",")
		opts, err := parseTagFlags(flags)
		//: a flag yaml.v3 does not know either.
		if err != "" {
			info.tagError = err
			return
		}
		index := append(slices.Clone(prefix), i)
		//: an inlined struct or map.
		if opts.inline {
			inlineField(info, field, index, visiting)
			continue
		}
		//: an unexported field can be neither read nor written.
		if !field.IsExported() {
			continue
		}
		//: the key: the tag's name, or the field's name in lower case.
		if name == "" {
			name = strings.ToLower(field.Name)
		}
		addField(info, fieldInfo{key: name, index: index, omitEmpty: opts.omitEmpty, flow: opts.flow})
	}
}

// yamlTag returns the field's yaml tag, and false when the field takes no
// part: unexported and not embedded, or tagged "-". Like yaml.v3, a field
// whose whole tag holds no ":" is read as a bare yaml tag.
func yamlTag(field reflect.StructField) (string, bool) {
	//: an unexported field takes part only when it is embedded (to be inlined).
	if !field.IsExported() && !field.Anonymous {
		//: no part.
		return "", false
	}
	tag := field.Tag.Get("yaml")
	//: the old bare form: `Name int "name"`.
	if tag == "" && !strings.Contains(string(field.Tag), ":") {
		tag = string(field.Tag)
	}
	//: "-" leaves the field out.
	if tag == "-" {
		//: no part.
		return "", false
	}
	//: the tag.
	return tag, true
}

// parseTagFlags reads the comma-separated flags of a yaml tag, returning the
// reason when one is unknown.
func parseTagFlags(flags string) (tagFlags, string) {
	var opts tagFlags
	//: no flags.
	if flags == "" {
		//: none.
		return opts, ""
	}
	//: each flag.
	for flag := range strings.SplitSeq(flags, ",") {
		switch flag {
		//: leave the field out when empty.
		case "omitempty":
			opts.omitEmpty = true
		//: write the collection in flow style.
		case "flow":
			opts.flow = true
		//: merge the field's keys into its parent.
		case "inline":
			opts.inline = true
		//: unknown.
		default:
			//: refused, as yaml.v3 refuses it.
			return opts, "a yaml struct tag holds an unknown flag"
		}
	}
	//: the flags.
	return opts, ""
}

// inlineField merges an ",inline" field into info: a struct's (or a pointer
// to a struct's) fields join the parent's, a map with string keys collects
// the keys no field declares.
func inlineField(info *structInfo, field reflect.StructField, index []int, visiting map[reflect.Type]bool) {
	t := field.Type
	//: a map collecting the other keys.
	if t.Kind() == reflect.Map {
		//: only one, and only with string keys.
		if info.inlineMap != nil || t.Key().Kind() != reflect.String {
			info.tagError = "an inline map must be the only one and have string keys"
			return
		}
		info.inlineMap = index
		return
	}
	//: through one pointer, which the decoder must be able to allocate.
	if t.Kind() == reflect.Pointer {
		//: an unexported field cannot be set.
		if !field.IsExported() {
			info.tagError = "an inlined pointer to a struct must be an exported field"
			return
		}
		t = t.Elem()
	}
	//: only a struct is inlined.
	if t.Kind() != reflect.Struct {
		info.tagError = "inline applies to a struct, a pointer to a struct, or a map"
		return
	}
	//: a cycle of inlined structs.
	if visiting[t] {
		info.tagError = "inlined structs form a cycle"
		return
	}
	visiting[t] = true
	buildStructInfo(info, t, index, visiting)
	delete(visiting, t)
}

// addField adds f to info, refusing a key two fields declare.
func addField(info *structInfo, f fieldInfo) {
	//: a duplicated key.
	if _, dup := info.byKey[f.key]; dup {
		info.tagError = "two fields of a struct declare the same yaml key"
		return
	}
	info.byKey[f.key] = len(info.fields)
	info.fields = append(info.fields, f)
}
