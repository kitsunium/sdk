package config

import (
	"maps"
	"slices"
	"strings"
	"unicode"
)

// foldKey is the case-folded form encoding/json compares a document's key to a
// field's name by: each rune lowered then raised, which folds the special
// cases (the Kelvin sign, the long s) the way the decoder does. Two keys with
// the same fold address the same field when no field carries either spelling
// exactly.
func foldKey(key string) string {
	//: the fold, rune by rune.
	return strings.Map(func(r rune) rune {
		//: lower then upper: the decoder's own folding.
		return unicode.ToUpper(unicode.ToLower(r))
	}, key)
}

// buildFolds indexes a vocabulary by the fold of each key, so a key a layer
// spelt in another case finds the one the type declares. A fold two declared
// keys share — fields tagged "port" and "Port" side by side — maps to "": the
// decoder tells those apart by the exact spelling, and so must the merge.
func buildFolds(known map[string]keyKind) map[string]string {
	folds := make(map[string]string, len(known))
	//: every declared key, once.
	for key := range known {
		folded := foldKey(key)
		//: a second key with the same fold makes the fold ambiguous.
		if _, taken := folds[folded]; taken {
			//: no canonical spelling to rewrite to.
			folds[folded] = ""
			continue
		}
		//: the one spelling this fold stands for.
		folds[folded] = key
	}
	//: the index a layer is canonicalised against.
	return folds
}

// keyVocabulary is what a merge canonicalises a layer against: the target
// type's keys, and the index of their folds.
type keyVocabulary struct {
	// known is every addressable key, marked leaf or table.
	known map[string]keyKind
	// folds maps the fold of a key to its declared spelling, "" when ambiguous.
	folds map[string]string
}

// resolve returns the declared spelling of the key name a layer supplied under
// base, its kind, and whether name is a known key at all. A name the type
// declares exactly is its own spelling; a name that only folds to one is
// rewritten to it; a name that contains the separator, folds to nothing or to
// an ambiguous pair, or folds to a key at another level is left as it is.
func (v keyVocabulary) resolve(base, name string) (canonical string, kind keyKind, found bool) {
	path := joinKey(base, name)
	//: the exact spelling: the decoder's first choice, and ours.
	if exact, ok := v.known[path]; ok {
		//: nothing to rewrite.
		return name, exact, true
	}
	//: a name with a separator is not one segment, and nothing to fold.
	if strings.Contains(name, keySeparator) {
		//: left as supplied.
		return name, keyLeaf, false
	}
	declared := v.folds[foldKey(path)]
	//: no declared key folds to it, or two do.
	if declared == "" {
		//: left as supplied.
		return name, keyLeaf, false
	}
	//: base is already canonical, so a key of this level is base, a
	//: separator, and one segment.
	prefix := ""
	//: below the root the level's keys share base's prefix.
	if base != "" {
		prefix = base + keySeparator
	}
	segment, sameLevel := strings.CutPrefix(declared, prefix)
	//: the fold landed on a key of another level.
	if !sameLevel || strings.Contains(segment, keySeparator) {
		//: left as supplied.
		return name, keyLeaf, false
	}
	//: rewritten to the spelling the type declares.
	return segment, v.known[declared], true
}

// canonicalLayer returns layer with every key that addresses a field of the
// target type in another case rewritten to the spelling the type declares, at
// every level the type names (ADR 0028: later wins, whatever the case).
//
// encoding/json matches a document's key to a field case-insensitively, so
// "Port" and "port" fill the same field; the merge, comparing keys exactly,
// kept both, and the decode's sorted order — not the layer order — then
// decided which one won (#468). Rewriting every layer to one spelling before
// the merge puts the two under one key, where the later layer overrides.
//
// A layer with nothing to rewrite is returned as it is, unallocated. Otherwise
// a copy is returned: the source's own map is never edited. Two keys of ONE
// layer that land on the same spelling fold in a fixed order — the variants in
// byte order, then the exact spelling — so the exact spelling wins, as it does
// in the decoder. The members of a leaf (a map, a type that decodes itself) are
// never rewritten: their keys are data, and "Env" and "env" are two of them.
func (v keyVocabulary) canonicalLayer(layer map[string]any, base string) map[string]any {
	//: nothing in this subtree is spelt differently from the type.
	if !v.needsRewrite(layer, base) {
		//: the layer itself, unallocated.
		return layer
	}
	//: every key of this level, in a fixed order, so the same layer
	//: canonicalises the same way every time.
	names := slices.Sorted(maps.Keys(layer))
	out := make(map[string]any, len(layer))
	//: the rewritten keys first, then the exact ones, so the exact spelling wins.
	for _, rewritten := range [...]bool{true, false} {
		//: every key, in byte order.
		for _, name := range names {
			canonical, kind, _ := v.resolve(base, name)
			//: this pass handles only one of the two groups.
			if (canonical != name) != rewritten {
				continue
			}
			value := layer[name]
			//: a table the type names has members to rewrite too.
			if sub, isTable := value.(map[string]any); isTable && kind == keyTable {
				value = v.canonicalLayer(sub, joinKey(base, canonical))
			}
			//: fold it in under its spelling; deepMerge clones a table, so the
			//: copy shares nothing with the source.
			deepMerge(out, map[string]any{canonical: value})
		}
	}
	//: the layer, in the type's spelling.
	return out
}

// needsRewrite reports whether some key of level, or of a table below it the
// type names, is spelt differently from the declared key it addresses.
func (v keyVocabulary) needsRewrite(level map[string]any, base string) bool {
	//: every key of this level.
	for name, value := range level {
		canonical, kind, found := v.resolve(base, name)
		//: a key spelt in another case.
		if canonical != name {
			//: rewrite.
			return true
		}
		//: descend only where the type names members.
		if sub, isTable := value.(map[string]any); isTable && found && kind == keyTable {
			//: a member below is spelt differently.
			if v.needsRewrite(sub, joinKey(base, name)) {
				//: rewrite.
				return true
			}
		}
	}
	//: every key already in the type's spelling, or unknown to it.
	return false
}
