// Package config — the merge + decode + validate loader.
package config

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"

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
	_, err := loadInto(target, nil, sources)
	//: the pipeline's verdict; the layers are the traced load's business.
	return err
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
	_, err := loadInto(target, schema, sources)
	//: the pipeline's verdict.
	return err
}

// loadInto is the one merge + keys + decode + check + validate pipeline every
// entry point runs, so a schema can never change the ORDER of the other steps
// — only whether the steps it owns happen at all. It returns the layers it
// merged, in merge order, so a traced load can say which one supplied a key.
func loadInto[T any](target *T, schema *SchemaValue[T], sources []coreconfig.Source) (loaded mergedLayers, err error) {
	//: the default layer under every source, later winning.
	merged, layers, mergeErr := mergeLayers(schema, sources)
	//: a source that could not be read aborts before anything is decoded.
	if mergeErr != nil {
		//: surface CONFIG_SOURCE_FAILED.
		return mergedLayers{}, mergeErr
	}
	//: a secret field is filled from the environment's RAW text, never from
	//: its JSON coercion, which would have re-spelled a numeric secret.
	restoreRawSecrets(merged, layers, secretKeysOf(schema))
	//: the KEY questions are answered while the keys are still keys.
	if schema != nil {
		//: every missing requirement and every stranger, in one error.
		if keyErr := schema.checkKeys(merged); keyErr != nil {
			//: surface CONFIG_KEY_MISSING and/or CONFIG_UNKNOWN_KEY.
			return mergedLayers{}, keyErr
		}
	}
	//: decode the merged map into the target via a JSON round-trip.
	if decodeErr := decodeInto(merged, target); decodeErr != nil {
		//: surface CONFIG_DECODE_FAILED.
		return mergedLayers{}, decodeErr
	}
	//: the VALUE questions, then the target's own self-check.
	if checkErr := checkDecoded(target, schema); checkErr != nil {
		//: surface CONFIG_VALIDATION_FAILED.
		return mergedLayers{}, checkErr
	}
	//: loaded; the layers and their fold are the traced load's to read.
	return mergedLayers{layers: layers, merged: merged}, nil
}

// mergedLayers is what a load folded: every layer in order, and the map they
// folded into — the one a traced load checks a key's final presence in.
type mergedLayers struct {
	// layers is every layer, in merge order.
	layers []layerValue
	// merged is the fold the decode read.
	merged map[string]any
}

// layerValue is one layer a load merged: what it supplied, and who.
type layerValue struct {
	// source supplied the layer; nil for the schema's default layer.
	source coreconfig.Source
	// position is the source's index in the caller's sources, counting from
	// 0, and -1 for the default layer.
	position int
	// values is the map the layer supplied.
	values map[string]any
}

// mergeLayers folds the schema's defaults and then every source into one map,
// and returns every layer it folded, in order.
func mergeLayers[T any](schema *SchemaValue[T], sources []coreconfig.Source) (merged map[string]any, layers []layerValue, err error) {
	//: merge all source layers into one map.
	merged = make(map[string]any, 0)
	layers = make([]layerValue, 0, len(sources)+1)
	//: the schema's defaults are the FIRST layer, so every source overrides
	//: them and none of them can be overridden BY one.
	if schema != nil {
		//: fold the default layer in; deepMerge copies, so the schema's own
		//: map is never reachable from the merged result.
		deepMerge(merged, schema.defaults)
		layers = append(layers, layerValue{position: -1, values: schema.defaults})
	}
	//: each source contributes a layer, later winning.
	for position, src := range sources {
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
				return nil, nil, loadErr
			}
			//: relabel anything else onto the documented sentinel.
			return nil, nil, wrapAs(coreconfig.ConfigSourceFailed, loadErr)
		}
		//: fold the layer in, and keep it for attribution.
		deepMerge(merged, layer)
		layers = append(layers, layerValue{source: src, position: position, values: layer})
	}
	//: one map, every layer folded, and the layers themselves.
	return merged, layers, nil
}

// secretKeysByType memoises the secret keys of every target type a schemaless
// load has decoded into, so the reflection walk happens once per TYPE rather
// than once per load — a Watcher replays a load on every change, and without
// the memo the walk doubled a schemaless load (measured in BENCH.md). It holds
// one entry per configuration type a program declares, which is a handful.
var secretKeysByType sync.Map

// secretKeysOf returns the keys of T whose field holds a secret, and how:
// compiled once into a schema, or walked once per type for a load that has
// none.
func secretKeysOf[T any](schema *SchemaValue[T]) map[string]secretHold {
	//: a schema resolved them at construction.
	if schema != nil {
		//: no walk per load.
		return schema.secrets
	}
	typ := reflect.TypeFor[T]()
	//: a type already walked by an earlier load.
	if cached, found := secretKeysByType.Load(typ); found {
		//: the memo only ever stores this shape, so the assertion holds.
		if secrets, ok := cached.(map[string]secretHold); ok {
			//: no walk.
			return secrets
		}
	}
	_, secrets := collectVocabulary(typ)
	//: two first loads racing store the same answer; either copy is right.
	secretKeysByType.Store(typ, secrets)
	//: walked once for this type.
	return secrets
}

// restoreRawSecrets replaces, for every DIRECT secret key an ENVIRONMENT layer
// supplied last, the JSON-coerced value in the merged map with the variable's
// raw text. A field holding secrets inside a slice or a map is left alone: its
// variable holds a JSON document — ["a","b"] — whose coerced form is exactly
// what its decode needs, and whose elements were written as strings.
//
// The coercion is right for a port and wrong for a secret: `12345` becomes an
// int64 and survives, but `1e3` becomes 1000, `0.10` becomes 0.1 and a
// twenty-digit token becomes a float64 that has lost its tail — a secret the
// operator never wrote, decoded without an error. core/secret.Value refuses a
// JSON number for exactly that reason, so without this step a numeric secret
// in the environment would fail the load; with it, the secret arrives as the
// operator typed it. Only a top-level key can be one variable; a secret nested
// in a table the environment supplied as JSON was written as JSON, quotes and
// all, and is decoded as written.
func restoreRawSecrets(merged map[string]any, layers []layerValue, secrets map[string]secretHold) {
	//: every secret key; the rest of the map is untouched.
	for key, hold := range secrets {
		//: a collection of secrets, or a key nested in a table, is not one
		//: variable holding one secret.
		if hold != secretDirect || strings.Contains(key, keySeparator) {
			continue
		}
		env, supplied := lastSupplier(layers, key)
		//: only the in-tree environment source coerces, and only it is undone.
		if !supplied {
			continue
		}
		//: the variable's raw text, read exactly as the source matched it.
		if _, raw, found := env.lookup(key); found {
			merged[key] = raw
		}
	}
}

// lastSupplier reports the environment source that supplied key LAST — the
// layer whose value the merge kept — or false when that layer is anything else.
func lastSupplier(layers []layerValue, key string) (env envSource, supplied bool) {
	//: the last layer holding the key is the one the merge kept.
	for _, layer := range slices.Backward(layers) {
		//: this layer did not supply the key; an earlier one may have.
		if _, holds := layer.values[key]; !holds {
			continue
		}
		env, supplied = layer.source.(envSource)
		//: the keeper, environment or not.
		return env, supplied
	}
	//: nobody supplied the key.
	return envSource{}, false
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
