// Package config — the default layer, seen as an ordinary Source.
package config

// schemaSource adapts a compiled default layer to the Source port.
type schemaSource struct {
	// defaults is the compiled layer, shared and never mutated.
	defaults map[string]any
}

// Load returns a private copy of the default layer. Defaults never fail to
// read: they were resolved at construction, and there is no backing store.
func (s schemaSource) Load() (values map[string]any, err error) {
	//: hand back a detached copy so a caller's mutation cannot reach the schema.
	return cloneNested(s.defaults), nil
}
