// Package config — resolving a declaration into a compiled schema.
//
// Everything in this file runs once, inside NewSchema, and every failure it
// reports is CONFIG_SCHEMA_INVALID: the author's declaration, not the
// operator's deployment.
package config

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
)

// compiledKeys is what a declaration resolves to once every key has been
// proven to name something in the target type.
type compiledKeys struct {
	// defaults is the normalised default layer, ready to merge.
	defaults map[string]any
	// declared is the defaulted keys, in declaration order.
	declared []string
	// required is the required keys, pre-split, in declaration order.
	required []requiredKey
	// known is the target type's whole addressable vocabulary.
	known map[string]keyKind
}

// compileKeys resolves a declaration's keys against T, refusing every one that
// could not work. Defaults are resolved first so the requirement check can see
// the whole defaulted set and refuse the pair that contradicts.
func compileKeys[T any](spec SchemaSpec[T]) (compiled compiledKeys, err error) {
	//: the addressable keys of the target type, in the operator's vocabulary.
	known := collectKeys(reflect.TypeFor[T]())
	//: the default layer, refusing what cannot travel or cannot be addressed.
	layer, declared, layerErr := buildDefaultLayer(spec.Defaults, known)
	//: a bad default stops here, at construction.
	if layerErr != nil {
		//: hand the typed refusal back.
		return compiledKeys{}, layerErr
	}
	//: the required keys, refusing what cannot be addressed or cannot fire.
	required, requiredErr := buildRequired(spec.Required, known, declared)
	//: a bad requirement stops here too.
	if requiredErr != nil {
		//: hand the typed refusal back.
		return compiledKeys{}, requiredErr
	}
	//: everything the compiled schema needs.
	return compiledKeys{defaults: layer, declared: declared, required: required, known: known}, nil
}

// buildDefaultLayer turns the declared defaults into the nested layer the merge
// folds under every Source, refusing every declaration that cannot work. It
// returns the layer and the declared keys in declaration order.
func buildDefaultLayer(defaults []coreconfig.DeclaredValue, known map[string]keyKind) (layer map[string]any, declared []string, err error) {
	//: the layer under construction, before its JSON normalisation.
	raw := make(map[string]any, len(defaults))
	//: declaration order is preserved so a refusal names the first offender.
	keys := make([]string, 0, len(defaults))
	//: duplicate detection is by key, not by position.
	seen := make(map[string]bool, len(defaults))
	//: every declaration is checked; the first failure refuses the schema.
	for _, def := range defaults {
		//: check and place this one.
		if placeErr := placeDefault(raw, def, known, seen); placeErr != nil {
			//: hand the typed refusal back.
			return nil, nil, placeErr
		}
		//: record the key for the contradiction check.
		keys = append(keys, def.Key)
	}
	//: normalise the layer through the loader's own round trip, so a struct
	//: default becomes a TABLE the merge can fold key-by-key. Without it a
	//: file supplying one member of a defaulted table would replace the whole
	//: table, silently dropping the members it did not mention.
	normalised, normErr := normaliseLayer(raw)
	//: a value that survived its own marshal cannot fail here, but a decode
	//: failure must never be assumed away.
	if normErr != nil {
		//: hand the typed refusal back.
		return nil, nil, normErr
	}
	//: the layer and what it declares.
	return normalised, keys, nil
}

// placeDefault validates one declaration and writes it into the layer.
func placeDefault(raw map[string]any, def coreconfig.DeclaredValue, known map[string]keyKind, seen map[string]bool) error {
	//: the grammar, the vocabulary and the duplicate check are the same three
	//: questions a required key answers, so they are asked in one place.
	segments, addressErr := addressKey(def.Key, known, seen)
	//: a key that names nothing is refused before its value is looked at.
	if addressErr != nil {
		//: hand the typed refusal back.
		return addressErr
	}
	//: prove the value can travel BEFORE the whole layer is marshalled, so the
	//: refusal can name which key was at fault.
	if _, marshalErr := json.Marshal(def.Value); marshalErr != nil {
		//: the value is never echoed — it may be a placeholder credential.
		return rejectSchema(def.Key, "the default value cannot be encoded as configuration")
	}
	//: mark before writing so a later duplicate is caught even if the write
	//: below is what fails.
	seen[def.Key] = true
	//: write into the nesting the key describes.
	if placed := nestKey(raw, segments, def.Value); !placed {
		//: one key cannot be both a value and a table.
		return rejectSchema(def.Key, "the key is declared both as a value and as a table")
	}
	//: accepted.
	return nil
}

// buildRequired resolves the required keys, refusing one that names nothing,
// one declared twice, and one the schema itself would fill.
func buildRequired(required []string, known map[string]keyKind, declared []string) (keys []requiredKey, err error) {
	//: nothing required is a legitimate schema — the accepting half of 0031.
	if len(required) == 0 {
		//: no presence pass to run.
		return nil, nil
	}
	//: declaration order is preserved so a report reads in the order written.
	resolved := make([]requiredKey, 0, len(required))
	//: duplicate detection is by key, independent of the defaults' own set.
	seen := make(map[string]bool, len(required))
	//: every requirement is checked; the first failure refuses the schema.
	for _, key := range required {
		//: same grammar, same vocabulary, same duplicate rule as a default.
		segments, addressErr := addressKey(key, known, seen)
		//: a requirement that names nothing could never be satisfied.
		if addressErr != nil {
			//: hand the typed refusal back.
			return nil, addressErr
		}
		//: a requirement the schema itself fills can never fire.
		if overlapErr := rejectDefaultedRequirement(key, declared); overlapErr != nil {
			//: hand the typed refusal back.
			return nil, overlapErr
		}
		//: mark and record, pre-split so no load parses this again.
		seen[key] = true
		//: keep the literal for the message and the segments for the lookup.
		resolved = append(resolved, requiredKey{key: key, segments: segments})
	}
	//: every requirement names something and can fire.
	return resolved, nil
}

// addressKey runs the three checks every declared key answers, whatever it
// declares: the grammar, the target type's vocabulary, and the duplicate.
func addressKey(key string, known map[string]keyKind, seen map[string]bool) (segments []string, err error) {
	//: the grammar first: a key that cannot be parsed names nothing.
	parts, wellFormed := splitKey(key)
	//: refuse by naming what the grammar is.
	if !wellFormed {
		//: the clause says what a key looks like.
		return nil, rejectSchema(key, "the key is empty, or has an empty segment; keys are dot-separated names")
	}
	//: the same key twice is a contradiction, not a last-wins.
	if seen[key] {
		//: refuse rather than pick one silently.
		return nil, rejectSchema(key, "the key is declared twice")
	}
	//: a key nothing reads is worse than no key: it looks configured.
	if _, addresses := known[key]; !addresses {
		//: name the type's vocabulary rather than guessing at the intent.
		return nil, rejectSchema(key, "the key names no field of the target type; keys use json tag names")
	}
	//: a well-formed, addressable, first-time key.
	return parts, nil
}

// rejectDefaultedRequirement refuses a key that is required AND defaulted — at
// the same level, or with one nested under the other.
//
// Either arrangement has the same consequence: the default layer supplies the
// key before any source is read, so the presence pass always finds it and the
// requirement is a clause that cannot fire. A clause that cannot fire is worse
// than no clause, because a reader takes it for protection.
func rejectDefaultedRequirement(key string, declared []string) error {
	//: any defaulted key that is this one, contains it, or lives under it.
	for _, other := range declared {
		//: an unrelated key is fine.
		if !keysOverlap(key, other) {
			//: keep looking.
			continue
		}
		//: name the requirement; the default's value is never echoed.
		return rejectSchema(key, "the key is declared both required and with a default")
	}
	//: the requirement can fire.
	return nil
}

// keysOverlap reports whether two keys address the same configuration, either
// because they are equal or because one is nested under the other.
func keysOverlap(left, right string) bool {
	//: the same key.
	if left == right {
		//: overlapping.
		return true
	}
	//: one under the other — the separator is required so "database" does not
	//: claim "database_replica".
	return strings.HasPrefix(left, right+keySeparator) || strings.HasPrefix(right, left+keySeparator)
}

// normaliseLayer sends the raw layer through the loader's JSON round trip so it
// holds exactly the shapes a Source would have produced. Numbers keep their
// exact literal (json.Number) rather than widening to float64, for the reason
// coerceEnvInto gives: a bare `any` decode loses integrality, and exactness
// above 2^53 with it.
func normaliseLayer(raw map[string]any) (layer map[string]any, err error) {
	//: an empty schema contributes an empty layer, not a nil one.
	if len(raw) == 0 {
		//: nothing to normalise.
		return make(map[string]any, 0), nil
	}
	//: every value was individually proven marshalable by placeDefault.
	encoded, marshalErr := json.Marshal(raw)
	//: fail loud rather than assume; an unencodable layer would decode to
	//: nothing and defaults would silently disappear.
	if marshalErr != nil {
		//: the key is unknown at this point — the clause carries the diagnosis.
		return nil, rejectSchema("", "the default layer cannot be encoded as configuration")
	}
	//: decode with number widening disabled.
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	//: json.Number keeps the exact literal through the second marshal.
	decoder.UseNumber()
	//: the normalised layer.
	out := make(map[string]any, len(raw))
	//: decode into it.
	if decodeErr := decoder.Decode(&out); decodeErr != nil {
		//: same fail-loud reason as above.
		return nil, rejectSchema("", "the default layer cannot be decoded as configuration")
	}
	//: ready to be merged under every Source.
	return out, nil
}

// rejectSelfContradiction refuses a schema whose own defaults do not satisfy
// the constraints it declares for them.
//
// The check is deliberately SCOPED to the declared keys. A defaults-only
// document is not a valid configuration — every key the operator is expected to
// supply is still at its Go zero — so a violation at a key nobody defaulted is
// the operator's to fix and says nothing about the schema. A violation at (or
// under) a key the schema itself filled is another matter entirely: the schema
// wrote that value, and it wrote a value its own rules reject.
func (s *SchemaValue[T]) rejectSelfContradiction() error {
	//: nothing was declared, so nothing can contradict.
	if len(s.declared) == 0 {
		//: an empty schema is consistent by construction.
		return nil
	}
	//: decode the defaults ALONE, through the same round trip a load uses —
	//: which is also what catches a default of the wrong type for its field.
	var probe T
	//: a decode failure here is a schema defect, never an operator's.
	if decodeErr := decodeInto(s.defaults, &probe); decodeErr != nil {
		//: name the clause; the offending value is never echoed.
		return rejectSchema("", "a default does not decode into the field it targets")
	}
	//: run the schema's own constraints over its own defaults.
	report := s.Check(probe)
	//: keep only what the schema is responsible for.
	for _, violation := range report {
		//: a violation outside every declared key belongs to the operator.
		if !coversPath(s.declared, violation.Path) {
			//: not a contradiction.
			continue
		}
		//: the rule name is the caller's own tag spelling — safe to echo, and
		//: it is what makes the refusal actionable without the value.
		return rejectSchema(violation.Path, "the default violates the "+violation.Rule+" rule the schema declares for this key")
	}
	//: every default satisfies the schema that declared it.
	return nil
}

// coversPath reports whether any declared key is the path itself or an ancestor
// of it. A default declared for a whole table owns the violations reported
// inside that table: the schema wrote those members.
func coversPath(declared []string, path string) bool {
	//: RootPath belongs to a cross-field rule, which a defaults-only document
	//: cannot satisfy meaningfully — every unsupplied key is still at its zero.
	if path == corevalidation.RootPath {
		//: not the schema's fault.
		return false
	}
	//: any declared key that is the path or a prefix level of it.
	for _, key := range declared {
		//: the exact key, or a member of a defaulted table — the separator is
		//: required so "database" does not claim "database_replica".
		if path == key || strings.HasPrefix(path, key+keySeparator) {
			//: covered.
			return true
		}
	}
	//: nobody declared a default here.
	return false
}
