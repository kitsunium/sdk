// Package config — the merge + decode + validate loader.
package config

import (
	"encoding/json"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Load merges every source (later overrides earlier) into target, then runs
// target.Validate() when target implements core/config.Validator. A source read
// failure, a decode failure, or a validation failure abort with the matching
// typed sentinel.
//
// It declares no schema: no key is required, no default is applied, and a key
// no field of T addresses is DROPPED by the decode exactly as encoding/json
// drops it. That is a legitimate way to load a configuration — there is nothing
// to contradict — and it is why the schema arrived as a second entry point
// rather than as a widened signature nobody could opt out of. A caller who
// wants the typo caught declares a schema and calls [LoadSchema].
func Load[T any](target *T, sources ...coreconfig.Source) error {
	//: no schema: no default layer, no key pass, and only the target's own
	//: Validate runs.
	return loadInto(target, nil, sources)
}

// LoadSchema is [Load] with a compiled [SchemaValue]: the schema's typed
// defaults are merged UNDER every source, its required keys and its vocabulary
// are checked against the merged map, and its constraints run over the decoded
// result before the target's own Validate.
//
// # The layer order, which is the whole contract
//
//	default  <  file  <  env  <  any later source
//
// The default layer is placed first STRUCTURALLY, not by argument order: there
// is no way to spell a load in which a default outranks a source, because a
// default that could win is not a default. Everything after it is the caller's
// order, so an explicit override is simply the last Source — the port is one
// method, and the SDK does not need a special type for "the values I decided
// at the call site".
//
// # An absent key and a key set to zero are different statements
//
// Defaults are a LAYER merged before the decode, so presence is decided while
// the operator's key still exists as a key. `timeout = 0` in a file is a key
// that was supplied and stays 0; an omitted `timeout` is a key that was not,
// and takes the default. A post-decode "if the field is zero, apply the
// default" cannot tell those apart, and would silently overrule the operator
// on every field whose zero value is meaningful — which is most of them.
//
// A partially-supplied table keeps its unmentioned defaults: the merge folds
// key-by-key, so a file setting `database.host` does not erase a default for
// `database.max_conns`.
//
// # The key pass runs first, and it runs once
//
// A required key no source supplied fails HERE, at start-up, before anything
// reads the value — that is the entire reason a schema exists. Every missing
// key is named in one error, together with every key the target type cannot
// address, so one restart tells an operator the whole truth.
//
// The key pass is not merged into the value report that follows it. A key
// nobody supplied decodes to a zero the operator never wrote, and running the
// bounds on that zero would report a rule nobody violated — burying the one
// fact that matters under a fact derived from it. The report is not truncated;
// the second pass is simply not run over input already known to be fiction.
//
// # What a failure says
//
// A violation is reported at the OPERATOR's key ("database.max_conns"), never
// at the Go field name, because the decode goes through a json round trip and
// the json tag is literally the key they typed. The error carries the count,
// the first rule and the offending keys; it carries no message and no value,
// because a configuration value may be a password, a token or a connection
// string. SchemaValue.Check returns the full report when messages are wanted.
//
// A nil schema is refused by name rather than silently loading nothing.
func LoadSchema[T any](target *T, schema *SchemaValue[T], sources ...coreconfig.Source) error {
	//: an inert schema would default nothing and check nothing while looking
	//: exactly like a working one — the ADR 0031 trap in its accepting form.
	if schema == nil {
		//: refuse before any source is read.
		return rejectSchema("", "the schema is nil; build one with NewSchema")
	}
	//: the schema contributes the bottom layer, the key pass and the rules.
	return loadInto(target, schema, sources)
}

// loadInto is the one merge + keys + decode + check + validate pipeline both
// entry points run, so a schema can never change the ORDER of the other steps —
// only whether the steps it owns happen at all.
func loadInto[T any](target *T, schema *SchemaValue[T], sources []coreconfig.Source) error {
	//: the default layer under every source, later winning.
	merged, mergeErr := mergeLayers(schema, sources)
	//: a source that could not be read aborts before anything is decoded.
	if mergeErr != nil {
		//: surface CONFIG_SOURCE_FAILED.
		return mergeErr
	}
	//: the KEY questions are answered while the keys are still keys.
	if schema != nil {
		//: every missing requirement and every stranger, in one error.
		if keyErr := schema.checkKeys(merged); keyErr != nil {
			//: surface CONFIG_KEY_MISSING and/or CONFIG_UNKNOWN_KEY.
			return keyErr
		}
	}
	//: decode the merged map into the target via a JSON round-trip.
	if err := decodeInto(merged, target); err != nil {
		//: surface CONFIG_DECODE_FAILED.
		return err
	}
	//: the VALUE questions, then the target's own self-check.
	return checkDecoded(target, schema)
}

// mergeLayers folds the schema's defaults and then every source into one map.
func mergeLayers[T any](schema *SchemaValue[T], sources []coreconfig.Source) (merged map[string]any, err error) {
	//: merge all source layers into one map.
	layers := make(map[string]any, 0)
	//: the schema's defaults are the FIRST layer, so every source overrides
	//: them and none of them can be overridden BY one.
	if schema != nil {
		//: fold the default layer in; deepMerge copies, so the schema's own
		//: map is never reachable from the merged result.
		deepMerge(layers, schema.defaults)
	}
	//: each source contributes a layer, later winning.
	for _, src := range sources {
		//: read the layer.
		layer, loadErr := src.Load()
		//: Source is a public interface, so a third-party implementation can
		//: return any error — including one carrying a foreign dotted-quad code.
		//: Normalise at the loader boundary so Load's typed contract holds
		//: whoever wrote the source. An error already carrying the sentinel's
		//: code propagates untouched rather than being double-wrapped.
		if loadErr != nil {
			//: in-tree sources already emit CONFIG_SOURCE_FAILED.
			if errs.HasCode(loadErr, coreconfig.CodeConfigSourceFailed) {
				//: already conformant — keep the original trail.
				return nil, loadErr
			}
			//: relabel anything else onto the documented sentinel.
			return nil, wrapAs(coreconfig.ConfigSourceFailed, loadErr)
		}
		//: fold the layer in.
		deepMerge(layers, layer)
	}
	//: one map, every layer folded.
	return layers, nil
}

// checkDecoded runs the schema's value constraints and then the target's own
// Validate. The order is deliberate: a cross-field assertion written by hand
// has no useful answer while the individual keys it reads are still invalid.
func checkDecoded[T any](target *T, schema *SchemaValue[T]) error {
	//: the schema's constraints, collect-all.
	if schema != nil {
		//: collect every violation, then convert.
		if report := schema.Check(*target); !report.OK() {
			//: surface CONFIG_VALIDATION_FAILED, located at the operator's keys.
			return rejectViolations(report)
		}
	}
	//: run optional self-validation. It is NOT replaced by the schema: a
	//: struct that implements Validator keeps being asked, and may implement
	//: it by calling SchemaValue.Check itself (ADR 0039 — a published port is
	//: not widened, and here it is not bypassed either).
	if validator, ok := any(target).(coreconfig.Validator); ok {
		//: a failing Validate aborts the load.
		if err := validator.Validate(); err != nil {
			//: surface CONFIG_VALIDATION_FAILED.
			return wrapAs(coreconfig.ConfigValidationFailed, err)
		}
	}
	//: fully loaded + validated.
	return nil
}

// decodeInto re-encodes the merged map to JSON then decodes it into target,
// reusing encoding/json's struct-tag mapping.
func decodeInto[T any](merged map[string]any, target *T) error {
	//: marshal the merged map.
	raw, err := json.Marshal(merged)
	//: a non-encodable map is a decode failure.
	if err != nil {
		//: surface CONFIG_DECODE_FAILED.
		return wrapAs(coreconfig.ConfigDecodeFailed, err)
	}
	//: decode into the typed target.
	if err := json.Unmarshal(raw, target); err != nil {
		//: surface CONFIG_DECODE_FAILED.
		return wrapAs(coreconfig.ConfigDecodeFailed, err)
	}
	//: decoded cleanly.
	return nil
}
