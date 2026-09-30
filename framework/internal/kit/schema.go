// Package kit — schemas: the JSON Schema of an operation's types.
package kit

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/codec/jsonshape"
)

var (
	// paramSources are the tags a request field is read by, besides its body.
	paramSources = []string{"path", "query", "header", "cookie"}

	// unnamed are the types a schema describes by what is written rather
	// than by name: a date-time, a duration, raw JSON.
	unnamed = namesOf(reflect.TypeFor[time.Time](), reflect.TypeFor[time.Duration](), reflect.TypeFor[json.RawMessage]())
)

// schemaOf describes how values of t look on the wire, as the SDK's jsonshape
// reads encoding/json: the members an object has and under which names, which
// may be omitted and which may be null.
func schemaOf(t reflect.Type) *model.Schema {
	return schemaFrom(jsonshape.Of(t), false)
}

// keptSchemaOf is schemaOf for a type kit keeps — a store's entity, a
// topic's message, a queued command's input —: with sealed, its fields that
// kit seals at rest say so (ADR 0006 §4).
func keptSchemaOf(t reflect.Type, sealed bool) *model.Schema {
	return schemaFrom(jsonshape.Of(t), sealed)
}

// requestSchemaOf describes a request type, saying where each field is read
// from: path, query, header, cookie or body. A field read from the path, the
// query, a header or a cookie is named by that tag, and is there even when
// encoding/json never writes it (json:"-"). Fields keep their declaration
// order.
func requestSchemaOf(t reflect.Type) *model.Schema {
	shape := jsonshape.Of(t)
	s := schemaFrom(shape, false)
	st := t
	for st.Kind() == reflect.Pointer {
		st = st.Elem()
	}
	if st.Kind() != reflect.Struct || shape.Kind != jsonshape.Object {
		return s
	}
	entries, written := bodyEntries(shape, s)
	entries = append(entries, paramEntries(st, written)...)
	slices.SortStableFunc(entries, func(a, b fieldEntry) int { return slices.Compare(a.index, b.index) })
	s.Fields = make([]model.Field, 0, len(entries))
	for _, e := range entries {
		s.Fields = append(s.Fields, e.field)
	}
	return s
}

// fieldEntry is a request field, where its struct declares it.
type fieldEntry struct {
	index []int
	field model.Field
}

// bodyEntries are the fields encoding/json writes — read from the body, or
// from where their tag says — and the indexes it wrote.
func bodyEntries(shape *jsonshape.Shape, s *model.Schema) (entries []fieldEntry, written map[string]bool) {
	entries = make([]fieldEntry, 0, len(shape.Fields))
	written = map[string]bool{}
	for i, f := range shape.Fields {
		written[fmt.Sprint(f.Index)] = true
		field := s.Fields[i]
		field.In = "body"
		if in, name := paramTag(reflect.StructField{Name: f.GoName, Tag: f.Tag}); in != "" {
			field.Name, field.In, field.Optional = name, in, in != "path"
		}
		entries = append(entries, fieldEntry{index: f.Index, field: field})
	}
	return entries, written
}

// paramEntries are the fields read from the path, the query, a header or a
// cookie that encoding/json does not write.
func paramEntries(st reflect.Type, written map[string]bool) []fieldEntry {
	var entries []fieldEntry
	for _, f := range reflect.VisibleFields(st) {
		if f.Anonymous || !f.IsExported() || written[fmt.Sprint(f.Index)] {
			continue
		}
		if in, name := paramTag(f); in != "" {
			field := model.Field{Name: name, In: in, Type: schemaOf(f.Type), Optional: in != "path", Rules: f.Tag.Get("validate")}
			classify(&field, f.Tag, false)
			entries = append(entries, fieldEntry{index: f.Index, field: field})
		}
	}
	return entries
}

// typeName is t's qualified name, "" for an unnamed type.
func typeName(t reflect.Type) (string, bool) {
	if t.Name() == "" || t.PkgPath() == "" {
		return "", false
	}
	return t.PkgPath() + "." + t.Name(), true
}

// schemaFrom is a wire shape as the model spells it.
func schemaFrom(sh *jsonshape.Shape, sealed bool) *model.Schema {
	s := &model.Schema{Type: sh.Kind.String(), Name: sh.Name, Format: sh.Format, Nullable: sh.Nullable, Ref: sh.Ref}
	if sh.Kind == jsonshape.Unsupported {
		s.Type = "any"
	}
	if unnamed[s.Name] {
		s.Name = ""
	}
	if sh.Items != nil {
		s.Items = schemaFrom(sh.Items, sealed)
	}
	if sh.Values != nil {
		s.Values = schemaFrom(sh.Values, sealed)
	}
	for _, f := range sh.Fields {
		member := schemaFrom(f.Shape, sealed)
		if omitsNil(f.Tag) {
			// A nil value is left out, never written null.
			member.Nullable = false
		}
		field := model.Field{Name: f.Name, Type: member, Optional: f.Optional, Rules: f.Rules}
		classify(&field, f.Tag, sealed)
		s.Fields = append(s.Fields, field)
	}
	return s
}

// omitsNil reports whether a field's json options leave a nil value out:
// omitempty and omitzero both do.
func omitsNil(tag reflect.StructTag) bool {
	_, opts, _ := strings.Cut(tag.Get("json"), ",")
	for opt := range strings.SplitSeq(opts, ",") {
		if opt == "omitempty" || opt == "omitzero" {
			return true
		}
	}
	return false
}

// paramTag says whether a request field is read from the path, the query, a
// header or a cookie, and under which name.
func paramTag(f reflect.StructField) (in, name string) {
	for _, src := range paramSources {
		if v, ok := f.Tag.Lookup(src); ok {
			n, _, _ := strings.Cut(v, ",")
			if n == "" {
				n = f.Name
			}
			return src, n
		}
	}
	return "", ""
}

// namesOf is the set of the qualified names of types.
func namesOf(types ...reflect.Type) map[string]bool {
	names := make(map[string]bool, len(types))
	for _, t := range types {
		if name, ok := typeName(t); ok {
			names[name] = true
		}
	}
	return names
}
