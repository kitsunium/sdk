// Package config — the key grammar a schema declares in, and its resolution
// against the target type.
//
// Everything here runs ONCE, inside NewSchema. Nothing in this file runs during
// a Load: a schema that compiled is a schema whose keys have already been
// proven to name something.
package config

import (
	"encoding"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
)

// keySeparator is the one level-separator of the key grammar. It is the same
// character core/validation.JoinField writes, so a default's key and a
// violation's path are literally the same string.
const keySeparator string = "."

// jsonTagName is the tag that names a field on the operator's surface. Load
// decodes every format — TOML, YAML, env, JSON — through a json round trip, so
// this tag is the key the operator typed, and the Go field name is not.
const jsonTagName string = "json"

// tagOptionSeparator splits a json tag's name from its options ("port,omitempty").
const tagOptionSeparator string = ","

// tagOptOut is the json tag value that removes a field from the wire entirely.
// A field that does not travel cannot carry a default.
const tagOptOut string = "-"

// expectedKeys is the capacity hint for a freshly-walked type. A configuration
// struct is a handful of keys, not a thousand; the hint exists to avoid the
// first two growth steps, not to be exact.
const expectedKeys int = 16

// expectedDepth is the capacity hint for the recursion guard — a configuration
// nests a few levels, and the guard holds one entry per level.
const expectedDepth int = 8

// keyKind tells apart the two things an addressable key can be. The
// distinction only matters for the unknown-key pass: it must descend into a
// table and must NOT descend into a leaf, because a leaf's members are not
// keys — they belong to a map, a slice, or a type that decodes itself.
type keyKind uint8

const (
	// keyLeaf is a key the decoder fills directly. Nothing below it is
	// addressable, so nothing below it can be unknown.
	keyLeaf keyKind = iota
	// keyTable is a key whose members are themselves addressable keys.
	keyTable
)

// jsonUnmarshalerType and textUnmarshalerType are the two interfaces that make
// a struct a LEAF for this walk. A type that decodes itself — time.Time,
// net.IP, a netip.Addr — receives raw bytes, so encoding/json never reaches its
// members and a key naming one of them would silently address nothing. This is
// the same trap ADR 0046 refuses to walk into automatically, decided here by a
// mechanical property of the type rather than by a list of known names.
var (
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// splitKey parses a declared key into its segments, reporting whether the
// grammar was respected. An empty key, an empty segment ("a..b"), and a leading
// or trailing separator are all refused: each of them would produce a nesting
// level with no name, which the merged map cannot represent and the operator
// cannot type.
func splitKey(key string) (segments []string, ok bool) {
	//: an empty key names no configuration at all.
	if key == "" {
		//: nothing to split.
		return nil, false
	}
	//: the grammar is one separator, so a plain split is the whole parse.
	parts := strings.Split(key, keySeparator)
	//: an empty segment comes from "..", a leading dot or a trailing one —
	//: each would produce a nesting level with no name.
	if slices.Contains(parts, "") {
		//: refuse the whole key rather than silently dropping a level.
		return nil, false
	}
	//: a well-formed key.
	return parts, true
}

// nestKey writes value into dst at the nesting the segments describe, creating
// the intermediate maps as it goes. It is how a flat declaration
// ("database.max_conns") becomes the nested layer deepMerge already knows how
// to fold under a file's own nested tables.
//
// It reports false when an intermediate level is already occupied by a
// non-map — two declared keys where one is the prefix of the other
// ("database" and "database.max_conns"), which is a contradiction in the
// schema rather than something to resolve by guessing.
func nestKey(dst map[string]any, segments []string, value any) bool {
	//: walk down to the parent of the leaf, creating levels on the way.
	level := dst
	//: every segment but the last names a nesting level.
	for _, segment := range segments[:len(segments)-1] {
		//: an existing level is descended into; anything else is a conflict.
		switch existing := level[segment].(type) {
		//: absent — create the level.
		case nil:
			//: a fresh nested map to descend into.
			next := make(map[string]any, 1)
			//: attach and descend.
			level[segment] = next
			//: continue below the new level.
			level = next
		//: already a level — descend into it.
		case map[string]any:
			//: reuse it so two keys under one table share the level.
			level = existing
		//: a scalar sits where a level is needed.
		default:
			//: refuse; the schema declares a key both as a leaf and as a table.
			return false
		}
	}
	//: the leaf segment.
	leaf := segments[len(segments)-1]
	//: a leaf landing on an existing level is the same contradiction, mirrored.
	if _, occupied := level[leaf].(map[string]any); occupied {
		//: refuse rather than overwrite a table with a scalar.
		return false
	}
	//: place the default.
	level[leaf] = value
	//: written.
	return true
}

// lookupKey reports whether the pre-split segments resolve to an entry that
// EXISTS in level, whatever that entry holds.
//
// Existence is the whole question, and it is the one only the merged map can
// answer: after the decode an absent key and a key set to its zero are the same
// bytes. An explicit null therefore counts as SUPPLIED — the operator wrote the
// key — and whether the resulting zero is acceptable is the validation domain's
// question, not this one. Keeping the two apart is what stops the schema from
// quietly becoming a second constraint engine.
func lookupKey(level map[string]any, segments []string) bool {
	//: walk down to the parent of the leaf.
	for _, segment := range segments[:len(segments)-1] {
		//: an absent or non-table level means the leaf cannot exist.
		next, isTable := level[segment].(map[string]any)
		//: nothing below a scalar.
		if !isTable {
			//: absent.
			return false
		}
		//: descend.
		level = next
	}
	//: the leaf either has an entry or it does not.
	_, exists := level[segments[len(segments)-1]]
	//: supplied, whatever it holds.
	return exists
}

// collectKeys resolves every dotted key the target type can be addressed by,
// in the json vocabulary the loader decodes through, and says of each whether
// it is a leaf or a table. It is what turns a typo in a declared key into a
// construction failure instead of a default that silently never applies — the
// quietest way a configuration can be wrong — and what lets the unknown-key
// pass stop descending where the type stops naming things.
//
// The walk stops at a type that decodes itself (see jsonUnmarshalerType) and at
// a type it has already entered, so a self-referential struct terminates. A key
// inside a recursive type therefore cannot be declared; that is a stated limit,
// not an oversight.
func collectKeys(typ reflect.Type) map[string]keyKind {
	//: one shared result: the walk appends, never merges.
	keys := make(map[string]keyKind, expectedKeys)
	//: track the struct types on the current path so a cycle terminates.
	visiting := make(map[reflect.Type]bool, expectedDepth)
	//: walk from the root, whose base path is the empty prefix.
	walkKeys(typ, "", keys, visiting)
	//: every addressable key.
	return keys
}

// walkKeys adds every key reachable below base into keys.
func walkKeys(typ reflect.Type, base string, keys map[string]keyKind, visiting map[reflect.Type]bool) {
	//: a pointer to a struct addresses the same members as the struct.
	target, _ := derefStruct(typ)
	//: only a struct has members to name, and a type already on the path
	//: would recurse without end — both mean "nothing to add here".
	if target.Kind() != reflect.Struct || visiting[target] {
		//: stop.
		return
	}
	//: mark for the subtree and unmark on the way out, so a diamond (two
	//: fields of the same type) is still fully walked on both branches.
	visiting[target] = true
	//: leaving this type's subtree.
	defer delete(visiting, target)
	//: every field contributes its own key, and possibly a subtree.
	for field := range target.Fields() {
		//: resolve and record this one.
		walkField(field, base, keys, visiting)
	}
}

// walkField records one field's key, and descends when the decoder would.
func walkField(field reflect.StructField, base string, keys map[string]keyKind, visiting map[reflect.Type]bool) {
	//: an unexported field never decodes — EXCEPT an embedded struct, whose
	//: exported members encoding/json promotes into the outer type whether or
	//: not the embedded type itself is exported. Measured, not assumed: an
	//: unexported embedded struct populates from the wire, and gains a level
	//: of its own when its json tag names it.
	if !field.IsExported() && !embeds(field) {
		//: skip it.
		return
	}
	//: resolve the name and whether the field travels at all.
	name, travels := keyName(field)
	//: a json:"-" field is absent from every document.
	if !travels {
		//: skip it.
		return
	}
	//: an embedded field with no json name is INLINED by encoding/json: its
	//: members appear at the PARENT level, so it contributes no key of its own
	//: and its subtree is walked under the same base.
	if field.Anonymous && !hasJSONName(field) {
		//: promote the embedded members to this level.
		walkKeys(field.Type, base, keys, visiting)
		//: the embedded field itself is not addressable.
		return
	}
	//: this field's own key.
	path := joinKey(base, name)
	//: a table is a key whose members are keys too; everything else is a leaf.
	table := descends(field.Type)
	//: record it — a default may target the field as a whole either way.
	keys[path] = kindOf(table)
	//: descend only into a struct the decoder itself will descend into.
	if table {
		//: the members below carry their own keys.
		walkKeys(field.Type, path, keys, visiting)
	}
}

// kindOf turns "the walk descends into it" into the recorded kind.
func kindOf(table bool) keyKind {
	//: a table's members are addressable.
	if table {
		//: descend on the unknown-key pass.
		return keyTable
	}
	//: a leaf owns everything below it.
	return keyLeaf
}

// joinKey extends base with one segment, in the same grammar splitKey parses.
func joinKey(base, segment string) string {
	//: at the root the segment IS the key.
	if base == "" {
		//: no separator to write.
		return segment
	}
	//: one level down.
	return base + keySeparator + segment
}

// keyName is the name a field carries on the operator's surface: its json tag
// when it has a usable one, else its Go name. travels is false for a field the
// json tag removes from the document entirely.
func keyName(field reflect.StructField) (name string, travels bool) {
	//: the tag as written.
	tag := field.Tag.Get(jsonTagName)
	//: no tag — the Go name is the only name there is.
	if tag == "" {
		//: it still travels; encoding/json uses the field name.
		return field.Name, true
	}
	//: the name is everything before the first option.
	declared, _, _ := strings.Cut(tag, tagOptionSeparator)
	//: the explicit opt-out removes the field from every document.
	if declared == tagOptOut {
		//: nothing can address it.
		return "", false
	}
	//: an empty name (`json:",omitempty"`) leaves the Go name in force.
	if declared == "" {
		//: fall back.
		return field.Name, true
	}
	//: the name the operator writes.
	return declared, true
}

// embeds reports whether a field is an anonymous struct — the one shape whose
// members encoding/json reaches through an unexported field.
func embeds(field reflect.StructField) bool {
	//: only an anonymous field promotes anything.
	if !field.Anonymous {
		//: an ordinary field.
		return false
	}
	//: a pointer to a struct embeds exactly like the struct.
	target, _ := derefStruct(field.Type)
	//: an embedded scalar (type MyInt int) has no members to promote.
	return target.Kind() == reflect.Struct
}

// hasJSONName reports whether a field's json tag names it explicitly. An
// embedded field with a name is a nested object; without one it is inlined.
func hasJSONName(field reflect.StructField) bool {
	//: resolve the name, ignoring the travels flag: json:"-" is not a name.
	tag := field.Tag.Get(jsonTagName)
	//: no tag at all — no explicit name.
	if tag == "" {
		//: inlined.
		return false
	}
	//: the part before the options.
	declared, _, _ := strings.Cut(tag, tagOptionSeparator)
	//: "-" removes the field; "" leaves it inlined.
	return declared != "" && declared != tagOptOut
}

// descends reports whether the walk should enter a field's type. Only a plain
// struct (or a pointer to one) qualifies: a type that decodes ITSELF receives
// raw bytes and never exposes its members to a key, and a map, slice or array
// has no statically-named members at all.
func descends(typ reflect.Type) bool {
	//: resolve one level of pointer.
	target, _ := derefStruct(typ)
	//: only a struct has members to name.
	if target.Kind() != reflect.Struct {
		//: a leaf.
		return false
	}
	//: a self-decoding type is opaque to the key grammar.
	return !decodesItself(target)
}

// decodesItself reports whether typ takes over its own decoding, which makes it
// a leaf however many exported fields it happens to have. time.Time is the
// canonical case, and the reason this is a property test rather than a list.
func decodesItself(typ reflect.Type) bool {
	//: the unmarshaler methods are declared on the POINTER receiver.
	pointer := reflect.PointerTo(typ)
	//: either interface is enough to make the type opaque.
	return pointer.Implements(jsonUnmarshalerType) || pointer.Implements(textUnmarshalerType)
}

// derefStruct resolves one level of pointer, so *Database is walked exactly
// like Database. deref reports whether a pointer was removed.
func derefStruct(typ reflect.Type) (target reflect.Type, deref bool) {
	//: only a pointer needs resolving.
	if typ.Kind() == reflect.Pointer {
		//: one level is enough — **T is not a shape the decoder produces.
		return typ.Elem(), true
	}
	//: already the target.
	return typ, false
}
