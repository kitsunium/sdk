// Package config — the default layer, seen as an ordinary Source.
package config

import coreconfig "github.com/kitsunium/sdk/internal/core/config"

// schemaSource adapts a compiled default layer to the Source port.
type schemaSource struct {
	// defaults is the compiled layer, shared and never mutated.
	defaults map[string]any
}

// Load returns a private copy of the default layer — every table and every
// array in it, not only the top-level map. Defaults never fail to read: they
// were resolved at construction, and there is no backing store.
func (s schemaSource) Load() (values map[string]any, err error) {
	//: hand back a detached copy so a caller's mutation, of a table or of an
	//: array, cannot reach the schema.
	return cloneNested(s.defaults), nil
}

// Describe implements core/config.Describer: every key it supplies is a
// schema default, and a default has no further detail.
func (s schemaSource) Describe(_ string) (layer, detail string) {
	//: the default layer, whichever key.
	return coreconfig.LayerDefault, ""
}
