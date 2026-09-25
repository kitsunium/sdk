// Package config — traced loads: the same pipeline, and for every key of the
// target which layer supplied its final value (ADR 0097).
package config

import (
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
)

// LoadWithOrigins is [Load], and it also reports where every key of T got its
// final value: one core/config.OriginValue per leaf key of T, sorted by key.
//
// An origin names the LAYER — "default", "file", "env", or the kind a source
// reports through core/config.Describer, else "source" with the source's
// position — and the DETAIL an operator acts on: the variable that set the
// key ("APP_DATA_DIR"), the file that holds it. It never carries the value,
// and a key whose field holds a core/secret.Value is marked Secret, so a
// --show-config rendering knows to mask whatever it prints beside it. A key
// absent from the merged layers — no layer supplied it, or a later one erased
// it by replacing one of its tables with a scalar or a null — is reported with
// an empty Layer: its field holds its zero.
//
// The load itself is exactly [Load]'s — same order, same verdicts — and on a
// failure no origin is returned: a configuration that did not load has no
// provenance worth reading.
func LoadWithOrigins[T any](target *T, sources ...coreconfig.Source) (origins []coreconfig.OriginValue, err error) {
	loaded, loadErr := loadInto(target, nil, sources)
	//: nothing loaded, nothing to attribute.
	if loadErr != nil {
		//: Load's own verdict.
		return nil, loadErr
	}
	known, secrets := collectVocabulary(reflect.TypeFor[T]())
	//: one origin per leaf key of T.
	return originsOf(known, secrets, loaded), nil
}

// LoadSchemaWithOrigins is [LoadSchema] with the origins [LoadWithOrigins]
// reports; a key the schema's defaults supplied is reported as "default". A
// nil schema is refused by name, as LoadSchema refuses it.
func LoadSchemaWithOrigins[T any](target *T, schema *SchemaValue[T], sources ...coreconfig.Source) (origins []coreconfig.OriginValue, err error) {
	//: an inert schema is refused here as it is by LoadSchema.
	if schema == nil {
		//: refused before any source is read.
		return nil, rejectSchema("", "the schema is nil; build one with NewSchema")
	}
	loaded, loadErr := loadInto(target, schema, sources)
	//: nothing loaded, nothing to attribute.
	if loadErr != nil {
		//: LoadSchema's own verdict.
		return nil, loadErr
	}
	//: the schema resolved the vocabulary and the secrets at construction.
	return originsOf(schema.known, schema.secrets, loaded), nil
}

// originsOf attributes every leaf key of the vocabulary to the LAST layer that
// supplied it — the layer whose value the merge kept — in key order.
func originsOf(known map[string]keyKind, secrets map[string]secretHold, loaded mergedLayers) []coreconfig.OriginValue {
	keys := slices.Sorted(maps.Keys(known))
	origins := make([]coreconfig.OriginValue, 0, len(keys))
	//: a table is not a value; its members are reported instead.
	for _, key := range keys {
		//: only a leaf is filled by the decoder directly.
		if known[key] != keyLeaf {
			continue
		}
		layer, detail := attribute(loaded, key)
		origins = append(origins, coreconfig.OriginValue{
			Key: key, Layer: layer, Detail: detail, Secret: secrets[key] != secretNone,
		})
	}
	//: sorted, so two loads of one deployment report identically.
	return origins
}

// attribute names the layer that supplied key's final value and asks it to
// describe itself. Both answers are empty when the key is absent from the
// merged fold — never supplied, or supplied and then erased by a later layer
// that replaced one of its tables with a scalar or a null: the field then
// holds its zero, and the layer that first supplied the key did not decide it.
func attribute(loaded mergedLayers, key string) (layer, detail string) {
	segments := strings.Split(key, keySeparator)
	//: absent from the fold: the decoder filled nothing, whoever once did.
	if !lookupKey(loaded.merged, segments) {
		//: unset.
		return "", ""
	}
	//: present in the fold, so the last layer holding it is the one the merge
	//: kept — a later layer that erased an ancestor would have removed it.
	for _, candidate := range slices.Backward(loaded.layers) {
		//: an explicit null counts as supplied, as it does for Required.
		if lookupKey(candidate.values, segments) {
			//: this layer's own account of itself.
			return describeLayer(candidate, key)
		}
	}
	//: unreachable while the fold is built from these layers.
	return "", ""
}

// describeLayer names one layer: the default layer, a source that describes
// itself, or a source that does not — named by its position.
func describeLayer(layer layerValue, key string) (name, detail string) {
	//: the schema's defaults carry no source.
	if layer.source == nil {
		//: a default has no further detail.
		return coreconfig.LayerDefault, ""
	}
	//: a source that says where its values come from.
	if describer, ok := layer.source.(coreconfig.Describer); ok {
		//: its own words.
		return describer.Describe(key)
	}
	//: a source that does not: its position in the caller's sources.
	return coreconfig.LayerSource, strconv.Itoa(layer.position)
}
