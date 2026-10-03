// Package validation — which field a JSON key actually decodes into.
package validation

import (
	"cmp"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// jsonTagPunctuation is the punctuation encoding/json accepts in a tag name;
// any other non-letter, non-digit rune makes it ignore the name.
const jsonTagPunctuation string = "!#$%&()*+-./:;<=>?@[]^_{|}~ "

// jsonCandidate is one field encoding/json considers for a key of an object:
// the key, the index path from the struct that owns the object, and whether
// the key came from a json tag rather than the Go name.
type jsonCandidate struct {
	name   string
	index  []int
	tagged bool
}

// checkJSONReach refuses a type in which a field carrying rules is one that
// encoding/json considers for a key and then decodes that key into another
// field, or into none.
//
// A path names a key, and config.Load decodes every format through a JSON
// round trip, so a rule is only meaningful on the field that key reaches. Two
// cases break that, both from encoding/json's own resolution of promoted
// fields: a shallower field of the same name HIDES a deeper one, and two
// fields at the same depth, both tagged or both not, make the key AMBIGUOUS,
// in which case JSON decodes it into neither and says nothing. A rule on the
// hidden or the ambiguous field judges a value no input can set, and reports
// it at a path whose key belongs to someone else — so an operator can neither
// satisfy it nor tell which field failed. A field JSON ignores outright
// (unexported, or json:"-") is not a candidate and is not judged here.
func checkJSONReach(typ reflect.Type) error {
	//: nothing embedded and every key distinct: each field is the only
	//: candidate for its key, so there is nothing to resolve — the common case,
	//: answered without the walk.
	if directKeysDistinct(typ) {
		//: every rule sits on a field its key reaches.
		return nil
	}
	candidates := jsonCandidates(typ)
	dominant := jsonDominant(candidates)
	considered := make(map[string]bool, len(candidates))
	//: every field JSON weighs for some key, dominant or not.
	for _, candidate := range candidates {
		//: keyed by index path, the one identity names cannot blur.
		considered[indexKey(candidate.index)] = true
	}
	//: every field this plan would put rules on at this object's level.
	for _, member := range constrainedMembers(typ, nil, map[reflect.Type]bool{}) {
		key := indexKey(member.index)
		//: weighed for a key, and the key went elsewhere.
		if considered[key] && !dominant[key] {
			//: InvalidRule, naming the field and why no input reaches it.
			return rejectRule(member.name, jsonTagName,
				"encoding/json decodes this key into another field or into none — a shallower field "+
					"of the same name hides this one, or one at its depth makes the key ambiguous")
		}
	}
	//: every rule sits on a field its key reaches.
	return nil
}

// directKeysDistinct reports whether typ embeds nothing and no two of its
// fields answer to the same JSON key. Either way round, a false sends the type
// through the full resolution, which is correct for every type; this only
// spares it the types where competition cannot exist.
func directKeysDistinct(typ reflect.Type) bool {
	keys := make([]string, 0, typ.NumField())
	//: every field, in declaration order.
	for field := range typ.Fields() {
		//: an embedding may promote fields, which only the walk can place.
		if field.Anonymous {
			//: resolve fully.
			return false
		}
		name, ignored := jsonKeyOf(field)
		//: a field JSON ignores competes for nothing.
		if ignored {
			//: next field.
			continue
		}
		//: no usable json name: the Go name is the key.
		if name == "" {
			name = field.Name
		}
		//: a second field answering to this key: a tie or a tie-break.
		if slices.Contains(keys, name) {
			//: resolve fully.
			return false
		}
		keys = append(keys, name)
	}
	//: one candidate per key.
	return true
}

// constrainedMembers lists the fields of typ that carry rules and belong to
// typ's own JSON object: its tagged fields, and the tagged fields of every
// promoted embedding it dives into, whose members are keys of the same
// object. visiting stops a self-embedding type from recursing forever; the
// plan's own compile refuses the cycle.
func constrainedMembers(typ reflect.Type, prefix []int, visiting map[reflect.Type]bool) []reachMember {
	//: a type already on this path contributes nothing new.
	if visiting[typ] {
		//: the cycle is refused by compileStruct, not here.
		return nil
	}
	var members []reachMember
	visiting[typ] = true
	//: a diamond (one type embedded twice) is walked once per path.
	defer delete(visiting, typ)
	//: in declaration order.
	for index := range typ.NumField() {
		field := typ.Field(index)
		raw := field.Tag.Get(tagName)
		//: no rule, or the opt-out: nothing is judged on this field.
		if raw == "" || raw == "-" {
			//: next field.
			continue
		}
		path := append(slices.Clone(prefix), index)
		//: a promoted embedding the plan dives into lends its members to
		//: this object; the embedding itself is a key of nothing.
		if promotedEmbedding(field) && slices.Contains(parseTag(raw), diveRule) {
			target, _ := derefType(field.Type)
			//: its members, at this object's level.
			members = append(members, constrainedMembers(target, path, visiting)...)
			//: next field.
			continue
		}
		//: an ordinary member, keyed at this level.
		members = append(members, reachMember{name: pathName(field), index: path})
	}
	//: the constrained members of this object.
	return members
}

// jsonCandidates gathers the fields encoding/json considers for the keys of
// typ's object, breadth-first through untagged embedded structs — typeFields
// in encoding/json/encode.go, re-derived because the package does not export
// it. A struct type queued twice at one depth contributes each of its fields
// twice, as there, so that the resolution sees the tie and annihilates it.
func jsonCandidates(typ reflect.Type) []jsonCandidate {
	var candidates []jsonCandidate
	var current []jsonLevel
	next := []jsonLevel{{typ: typ}}
	count, nextCount := map[reflect.Type]int{}, map[reflect.Type]int{}
	visited := map[reflect.Type]bool{}
	//: one depth per round; this round's queue counts become the next one's.
	for len(next) > 0 {
		current, next = next, current[:0]
		count, nextCount = nextCount, count
		clear(nextCount)
		//: every embedded struct queued at this depth.
		for _, level := range current {
			//: a type met at a shallower depth has already given its fields.
			if visited[level.typ] {
				//: next level.
				continue
			}
			visited[level.typ] = true
			candidates, next = scanJSONLevel(level, count[level.typ] > 1, candidates, next, nextCount)
		}
	}
	//: every field weighed, duplicates included.
	return candidates
}

// scanJSONLevel adds one embedded struct's fields to candidates, duplicating
// each when the type was queued more than once, and queues onto next the
// untagged embedded structs to explore at the following depth.
func scanJSONLevel(
	level jsonLevel, twice bool, candidates []jsonCandidate, next []jsonLevel, nextCount map[reflect.Type]int,
) (grown []jsonCandidate, queued []jsonLevel) {
	grown, queued = candidates, next
	//: in declaration order.
	for index := range level.typ.NumField() {
		candidate, embedded, keep := jsonCandidateOf(level.typ.Field(index), level.index, index)
		//: a field JSON ignores outright.
		if !keep {
			//: next field.
			continue
		}
		//: an untagged embedded struct is explored, not keyed.
		if embedded != nil {
			nextCount[embedded]++
			//: queued once per depth; the count remembers the rest.
			if nextCount[embedded] == 1 {
				queued = append(queued, jsonLevel{typ: embedded, index: candidate.index})
			}
			//: next field.
			continue
		}
		grown = append(grown, candidate)
		//: a second copy, so the resolution sees the tie.
		if twice {
			grown = append(grown, candidate)
		}
	}
	//: the level's candidates and the embeddings below it.
	return grown, queued
}

// jsonCandidateOf decides what encoding/json does with one field: ignore it
// (keep false), explore it as an untagged embedded struct (embedded set), or
// weigh it for a key (candidate).
func jsonCandidateOf(field reflect.StructField, parent []int, index int) (candidate jsonCandidate, embedded reflect.Type, keep bool) {
	name, ignored := jsonKeyOf(field)
	//: a field JSON never decodes.
	if ignored {
		//: dropped.
		return jsonCandidate{}, nil, false
	}
	path := append(slices.Clone(parent), index)
	fieldType := field.Type
	//: JSON follows an unnamed pointer to find the struct, and no further.
	if fieldType.Name() == "" && fieldType.Kind() == reflect.Pointer {
		fieldType = fieldType.Elem()
	}
	//: an untagged embedded struct lends its fields to this object.
	if name == "" && field.Anonymous && fieldType.Kind() == reflect.Struct {
		//: explored at the next depth.
		return jsonCandidate{index: path}, fieldType, true
	}
	//: no usable json name: the Go name is the key.
	if name == "" {
		//: weighed under its Go name.
		return jsonCandidate{name: field.Name, index: path}, nil, true
	}
	//: weighed under its tag's name.
	return jsonCandidate{name: name, index: path, tagged: true}, nil, true
}

// jsonKeyOf returns the name field's json tag gives it — "" when encoding/json
// would use none — and whether encoding/json ignores the field outright: an
// unexported field that is not an embedded struct, or the json:"-" opt-out.
func jsonKeyOf(field reflect.StructField) (name string, ignored bool) {
	target, _ := derefType(field.Type)
	tag := field.Tag.Get(jsonTagName)
	//: never decoded: unexported and nothing JSON can reach through, or opted out.
	if !field.IsExported() && (!field.Anonymous || target.Kind() != reflect.Struct) || tag == "-" {
		//: ignored.
		return "", true
	}
	name, _, _ = strings.Cut(tag, tagSeparator)
	//: a name JSON would not accept is no name at all.
	if !validJSONTagName(name) {
		//: weighed under the Go name.
		return "", false
	}
	//: the tag's name.
	return name, false
}

// jsonDominant resolves every key to the one candidate encoding/json decodes
// it into — dominantField in encoding/json/encode.go: the shallowest wins, a
// tagged one breaks a tie at that depth, and a tie that remains annihilates
// the key. It returns the winners' index paths.
func jsonDominant(candidates []jsonCandidate) map[string]bool {
	sorted := slices.Clone(candidates)
	slices.SortFunc(sorted, compareJSONCandidates)
	dominant := make(map[string]bool, len(sorted))
	//: one run of equal names per iteration.
	for start := 0; start < len(sorted); {
		end := start + 1
		//: the run of candidates for this key.
		for end < len(sorted) && sorted[end].name == sorted[start].name {
			end++
		}
		first := sorted[start]
		//: alone, or shallower or better tagged than the runner-up.
		if end-start == 1 || len(sorted[start+1].index) != len(first.index) || sorted[start+1].tagged != first.tagged {
			dominant[indexKey(first.index)] = true
		}
		start = end
	}
	//: the fields keys decode into.
	return dominant
}

// compareJSONCandidates is encoding/json's order: by name, then depth, then
// tagged before untagged, then index path.
func compareJSONCandidates(left, right jsonCandidate) int {
	//: the key first.
	if byName := strings.Compare(left.name, right.name); byName != 0 {
		//: different keys.
		return byName
	}
	//: shallower first.
	if byDepth := cmp.Compare(len(left.index), len(right.index)); byDepth != 0 {
		//: different depths.
		return byDepth
	}
	//: tagged first.
	if left.tagged != right.tagged {
		//: the tagged one sorts first.
		if left.tagged {
			//: left is tagged.
			return -1
		}
		//: right is tagged.
		return 1
	}
	//: declaration order breaks what remains.
	return slices.Compare(left.index, right.index)
}

// validJSONTagName is encoding/json's isValidTag: a name made of letters,
// digits and the punctuation it allows.
func validJSONTagName(name string) bool {
	//: an empty name is no name.
	if name == "" {
		//: rejected.
		return false
	}
	//: every rune must be one JSON accepts in a key it takes from a tag.
	for _, char := range name {
		//: letters, digits and the allowed punctuation.
		if !strings.ContainsRune(jsonTagPunctuation, char) && !unicode.IsLetter(char) && !unicode.IsDigit(char) {
			//: rejected.
			return false
		}
	}
	//: accepted.
	return true
}

// indexKey renders an index path as a map key.
func indexKey(index []int) string {
	parts := make([]string, len(index))
	//: one decimal per level.
	for position, value := range index {
		parts[position] = strconv.Itoa(value)
	}
	//: dotted, which no index contains.
	return strings.Join(parts, ".")
}
