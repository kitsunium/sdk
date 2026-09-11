// Package config — the compiled schema: typed defaults, required keys, the
// unknown-key policy, and the constraints the decoded configuration must
// satisfy.
//
// A schema is compiled ONCE and refused at construction when it contradicts
// either the type it describes or itself. Nothing here decides anything at load
// time that could have been decided here.
package config

import (
	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
	svcvalidation "github.com/kitsunium/sdk/internal/service/validation"
)

// SchemaValue is a compiled configuration shape: the default LAYER it
// contributes, the keys it requires, the vocabulary it accepts, and the
// constraint the decoded configuration must satisfy. Build one with
// [NewSchemaValue]; the zero value is not usable and [LoadSchema] refuses a nil one
// by name rather than loading nothing and reporting success.
//
// It is safe for concurrent use and is meant to be built once, at start-up,
// and kept. pkg/v1/config.Schema aliases it.
type SchemaValue[T any] struct {
	// defaults is the layer this schema contributes, already through the
	// loader's JSON round trip so it holds exactly what a Source would have
	// produced. It is merged UNDER every Source, which is what makes a default
	// a default rather than a competitor.
	defaults map[string]any
	// declared is every key a default was declared for, in declaration order.
	// It is what the construction-time contradiction check is scoped to: a
	// violation at a key nobody defaulted is the operator's to fix, while a
	// violation at a key the schema itself filled is the schema's.
	declared []string
	// required is every key a load must find, pre-split at construction so the
	// presence pass parses nothing.
	required []requiredKey
	// known is the target type's whole vocabulary, with each key marked leaf
	// or table. The unknown-key pass walks the merged map against it.
	known map[string]keyKind
	// strict refuses a key outside `known`. It is the INVERSE of the spec's
	// AllowUnknownKeys so that the zero value of the spec is the strict one.
	strict bool
	// constraint is the `validate` tags of T composed with SchemaSpec.Rules,
	// collect-all. It is compiled once here, never per load.
	constraint corevalidation.Constraint[T]
}

// NewSchemaValue compiles spec into a [SchemaValue], refusing at CONSTRUCTION every
// declaration that could not work — which is the whole point of having one.
//
// Refused with CONFIG_SCHEMA_INVALID, each naming the key and the clause:
//
//   - a key outside the grammar (empty, an empty segment, a leading or
//     trailing separator);
//   - a key that names no field of T — a typo in a declared key would
//     otherwise produce a default that silently never applies, or a
//     requirement that can never be satisfied, which are the two quietest ways
//     a configuration can be wrong;
//   - the same key declared twice, or declared both as a value and as a table;
//   - a key declared both REQUIRED and with a default, in either order and at
//     either nesting level. The schema would fill the key itself, so the
//     requirement could never fire, and a clause that cannot fire is read as
//     protection;
//   - a default the loader's JSON round trip cannot carry, or that does not
//     decode into the field it targets ("eighty" for an int port);
//   - a default that violates the constraint the schema itself declares for
//     that key. That is ADR 0031's exact trap: a schema whose default sits
//     outside its own bounds produces an invalid configuration on precisely
//     the deployment where nobody set the key, and reports it as the
//     OPERATOR's fault.
//
// A tag the validation domain refuses surfaces THAT domain's error unchanged
// (INVALID_RULE / CONSTRAINT_MISCONFIGURED), not a config code: its fields
// already name the field, the rule and the clause, and relabelling would throw
// away the diagnosis to gain a code the caller has to look up anyway.
//
// A T with no `validate` tag, no Required and no Default is a valid, accepting
// schema — which still refuses an unknown key.
func NewSchemaValue[T any](spec SchemaSpec[T]) (schema *SchemaValue[T], err error) {
	//: the tag front end is compiled first: a refused tag is a source defect,
	//: and reporting it before any key resolution keeps the two diagnoses
	//: from being interleaved.
	tagRules, tagErr := svcvalidation.Struct[T](svcvalidation.StructConfig{})
	//: propagate the validation domain's own refusal, fields intact.
	if tagErr != nil {
		//: nothing is compiled and nothing is cached.
		return nil, tagErr
	}
	//: tags first, then the caller's cross-field rule — composition order is
	//: report order, and the per-field answers read better before the global
	//: one. Collect-all is not configurable here: an operator who restarts a
	//: service to discover the next bad key is the failure a report prevents.
	constraint := composeRule(tagRules, spec.Rule)
	//: resolve every declared key against the target type, refusing what
	//: cannot work — the defaults, the requirements, and their contradictions.
	compiled, keyErr := compileKeys[T](spec)
	//: a bad declaration stops here, at construction.
	if keyErr != nil {
		//: hand the typed refusal back.
		return nil, keyErr
	}
	//: the schema is assembled before its own consistency is checked, because
	//: the check runs the very machinery being assembled.
	built := &SchemaValue[T]{
		defaults:   compiled.defaults,
		declared:   compiled.declared,
		required:   compiled.required,
		known:      compiled.known,
		strict:     !spec.AllowUnknownKeys,
		constraint: constraint,
	}
	//: a schema that contradicts itself must not be handed to a caller.
	if selfErr := built.rejectSelfContradiction(); selfErr != nil {
		//: hand the typed refusal back.
		return nil, selfErr
	}
	//: compiled, consistent, and safe to share.
	return built, nil
}

// composeRule folds the caller's cross-field rule into the tag constraint. A
// caller who declared none pays no wrapper at all, which is the common case.
func composeRule[T any](tagRules, extra corevalidation.Constraint[T]) corevalidation.Constraint[T] {
	//: nothing to compose.
	if extra == nil {
		//: the tags alone.
		return tagRules
	}
	//: both, collect-all, in declaration order.
	return svcvalidation.All(tagRules, extra)
}

// Source returns the default LAYER as an ordinary [coreconfig.Source]. It is
// exposed so a caller can inspect the effective defaults — printing them for a
// `--show-config` flag, or diffing two releases — not so a caller can order
// them: [LoadSchema] always merges this layer first, and a default that could
// be placed after a file would not be a default.
//
// Each Load returns a fresh copy, so a caller that mutates the map it was
// handed cannot change what the next load sees.
func (s *SchemaValue[T]) Source() coreconfig.Source {
	//: a stateless reader over the compiled layer.
	return schemaSource{defaults: s.defaults}
}

// Check runs the schema's VALUE constraints against an already-decoded value
// and returns the FULL located report — every violation, at the operator's key.
//
// It answers only the value question. The KEY questions — was a required key
// supplied, did a source carry a key nothing reads — are not answerable from a
// decoded T at all: after the decode an absent key and a key set to its zero
// are the same bytes. Those are decided during [LoadSchema], on the merged map,
// and they are the reason this domain has a schema rather than a validator.
//
// It is what makes core/config.Validator a consumer of this engine rather than
// a competitor of it: a struct that wants to keep its own Validate method
// implements it in one line, and [LoadSchema] still calls that method after
// running the same constraints itself.
//
//	func (c Conf) Validate() error { return appSchema.Check(c).Err() }
//
// The report carries messages the error cannot; the error carries the count,
// the first rule and the offending keys. Neither carries a value.
func (s *SchemaValue[T]) Check(value T) corevalidation.ReportValue {
	//: run from the root — a top-level key's path is its own name.
	return s.constraint(corevalidation.RootPath, value)
}

// checkKeys runs the schema's KEY pass over the merged map, before any decode.
// It is one pass and it collects everything: every required key no source
// supplied, and every key the target type cannot address.
//
// Both answers are reported together, joined, so one restart tells an operator
// the whole truth. They are not merged with the VALUE report that follows,
// because a missing key decodes to a zero the operator never wrote — running
// the bounds on it would name a rule nobody violated and bury the one fact that
// matters under a derived one.
func (s *SchemaValue[T]) checkKeys(merged map[string]any) error {
	//: every required key that no layer supplied, in declaration order.
	missing := missingKeys(merged, s.required)
	//: every supplied key the type cannot address — skipped entirely when the
	//: author opted out, so an unprefixed environment costs no walk at all.
	unknown := unknownKeys(merged, s.known, s.strict)
	//: one error carrying both answers, or a genuine nil.
	return rejectKeys(missing, unknown)
}
