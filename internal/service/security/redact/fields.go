// Package redact — the members encoding/json writes for a struct, selected by
// encoding/json's own rules rather than an approximation of them.
package redact

import (
	"cmp"
	"reflect"
	"slices"
	"strings"
)

// candidate is one field encoding/json may write under name: index is its path
// from the struct being planned (reflect.Type.FieldByIndex), len(index) its
// depth, and tagged reports that the name came from its json tag.
type candidate struct {
	name   string
	index  []int
	tagged bool
}

// writtenField is a member encoding/json writes: its name and the field whose
// value it writes.
type writtenField struct {
	name  string
	field reflect.StructField
}

// embedding is a struct type whose fields are promoted into the struct being
// planned, and the index path that reaches it.
type embedding struct {
	typ   reflect.Type
	index []int
}

// writtenFields returns, for struct t, the one field encoding/json writes under
// each member name, in name order.
//
// The plan applies to what json.Marshal wrote, so it must select exactly the
// field json.Marshal selected — an approximation is a secret applied to the
// wrong value, or not applied at all. This is encoding/json's own resolution:
// Go's rules for embedded fields, amended so a tagged field wins a tie.
//
//   - Embedded structs are expanded one LEVEL at a time, shallowest first, and
//     each type once, at its shallowest level.
//   - For one name, only the fields at the smallest depth compete. One wins
//     alone; of several, a single tagged one wins; otherwise NONE is written —
//     and a deeper field of that name is not written either.
//   - A struct type embedded twice at one level has its fields counted twice,
//     so they tie and cancel out.
//   - An embedded struct is promoted by its kind alone. One that writes its own
//     JSON only reaches here when its method was not promoted — two such
//     structs at one level — and encoding/json then flattens both.
func writtenFields(t reflect.Type) []writtenField {
	var found []candidate
	visited := map[reflect.Type]bool{}
	next := []embedding{{typ: t}}
	// queued counts, per type, how many times the level being scanned embeds
	// it — above one, its fields tie with themselves — and counted tallies
	// the next level. The two are swapped, not reallocated, level to level.
	queued, counted := map[reflect.Type]int{}, map[reflect.Type]int{}
	//: one level of embedding at a time, the struct itself first.
	for len(next) > 0 {
		current := next
		next = nil
		//: every struct embedded at this level.
		for _, level := range current {
			//: a type expanded at a shallower level adds nothing deeper.
			if visited[level.typ] {
				continue
			}
			visited[level.typ] = true
			found, next = scanLevel(level, queued[level.typ] > 1, found, next, counted)
		}
		queued, counted = counted, queued
		clear(counted)
	}
	winners := dominant(found)
	written := make([]writtenField, len(winners))
	//: the field behind each winner, reached through its embeddings.
	for i, winner := range winners {
		written[i] = writtenField{name: winner.name, field: t.FieldByIndex(winner.index)}
	}
	//: one field per name, and none for a name encoding/json leaves out.
	return written
}

// scanLevel records the fields of one embedded struct and queues the structs
// it embeds for the next level. twice duplicates every recorded field, for a
// type embedded more than once at this level. counted tallies what the next
// level embeds.
func scanLevel(level embedding, twice bool, found []candidate, next []embedding, counted map[reflect.Type]int) ([]candidate, []embedding) {
	//: every field, in declaration order.
	for i := range level.typ.NumField() {
		field := level.typ.Field(i)
		name, tagged, promoted, visible := jsonName(field)
		//: encoding/json does not write it.
		if !visible {
			continue
		}
		index := append(slices.Clone(level.index), i)
		//: an embedded struct without a name: its fields join the next level.
		if promoted {
			embedded := unnamedElem(field.Type)
			counted[embedded]++
			//: queued once, counted every time.
			if counted[embedded] == 1 {
				next = append(next, embedding{typ: embedded, index: index})
			}
			continue
		}
		member := candidate{name: name, index: index, tagged: tagged}
		found = append(found, member)
		//: twice embedded at one level: the copy makes the tie that cancels it.
		if twice {
			found = append(found, member)
		}
	}
	//: this level's fields, and the structs of the next.
	return found, next
}

// dominant keeps, for each name, the field encoding/json writes, and drops the
// names it writes no field for.
func dominant(found []candidate) []candidate {
	slices.SortFunc(found, byPrecedence)
	written := make([]candidate, 0, len(found))
	//: one group of fields per name.
	for start := 0; start < len(found); {
		end := start + 1
		//: the fields that share the first one's name.
		for end < len(found) && found[end].name == found[start].name {
			end++
		}
		first := found[start]
		//: the first wins unless the second is as shallow and as tagged.
		if end-start == 1 || len(found[start+1].index) != len(first.index) || found[start+1].tagged != first.tagged {
			written = append(written, first)
		}
		start = end
	}
	//: in name order.
	return written
}

// byPrecedence orders fields by name, then depth, then tagged first, then
// declaration order — so the first of a name is the one that can win.
func byPrecedence(a, b candidate) int {
	//: grouped by name.
	if byName := strings.Compare(a.name, b.name); byName != 0 {
		//: different names never compete.
		return byName
	}
	//: the shallower first.
	if byDepth := cmp.Compare(len(a.index), len(b.index)); byDepth != 0 {
		//: depth decides.
		return byDepth
	}
	//: a tagged field before an untagged one at the same depth.
	if a.tagged != b.tagged {
		//: the tagged one first.
		if a.tagged {
			return -1
		}
		//: the untagged one second.
		return 1
	}
	//: declaration order breaks the rest.
	return slices.Compare(a.index, b.index)
}

// jsonName returns the member name encoding/json writes field under and
// whether it came from the json tag, whether the field is an embedded struct
// whose fields are promoted instead, and whether it is written at all.
func jsonName(field reflect.StructField) (name string, tagged, promoted, visible bool) {
	tag := field.Tag.Get("json")
	//: `json:"-"` is never written — `json:"-,"` is a member named "-", the
	//: whole tag being compared as encoding/json compares it — and neither is
	//: an unexported field, except an embedded STRUCT, whose exported fields
	//: are, whatever its own type's name.
	if tag == "-" || (!field.IsExported() && (!field.Anonymous || unnamedElem(field.Type).Kind() != reflect.Struct)) {
		//: invisible.
		return "", false, false, false
	}
	tagName, _, _ := strings.Cut(tag, ",")
	//: an embedded struct with no name of its own is flattened into its
	//: parent, through a pointer, whatever it implements.
	if tagName == "" && field.Anonymous && unnamedElem(field.Type).Kind() == reflect.Struct {
		//: promoted.
		return "", false, true, true
	}
	//: the Go name when the tag gives none.
	if tagName == "" {
		//: a member of its own, untagged.
		return field.Name, false, false, true
	}
	//: a member of its own, named by its tag — an unexported embedded struct
	//: included, which encoding/json writes whole under that name.
	return tagName, true, false, true
}

// unnamedElem returns what an unnamed pointer type points to, and any other
// type unchanged: the type whose kind decides whether a field is promoted.
func unnamedElem(t reflect.Type) reflect.Type {
	//: *S is followed; a named pointer type is a type of its own.
	if t.Name() == "" && t.Kind() == reflect.Pointer {
		//: the element.
		return t.Elem()
	}
	//: unchanged.
	return t
}
