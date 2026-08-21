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
func Load[T any](target *T, sources ...coreconfig.Source) error {
	//: merge all source layers into one map.
	merged := make(map[string]any, 0)
	//: each source contributes a layer, later winning.
	for _, src := range sources {
		//: read the layer.
		layer, err := src.Load()
		//: Source is a public interface, so a third-party implementation can
		//: return any error — including one carrying a foreign dotted-quad code.
		//: Normalise at the loader boundary so Load's typed contract holds
		//: whoever wrote the source. An error already carrying the sentinel's
		//: code propagates untouched rather than being double-wrapped.
		if err != nil {
			//: in-tree sources already emit CONFIG_SOURCE_FAILED.
			if errs.HasCode(err, coreconfig.CodeConfigSourceFailed) {
				//: already conformant — keep the original trail.
				return err
			}
			//: relabel anything else onto the documented sentinel.
			return wrapAs(coreconfig.ConfigSourceFailed, err)
		}
		//: fold the layer in.
		deepMerge(merged, layer)
	}
	//: decode the merged map into the target via a JSON round-trip.
	if err := decodeInto(merged, target); err != nil {
		//: surface CONFIG_DECODE_FAILED.
		return err
	}
	//: run optional self-validation.
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
