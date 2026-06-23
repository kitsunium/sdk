// Package config declares the configuration port of the SDK: a Source that
// yields a flat key→value map, a Validator the decoded struct may implement, and
// a Watcher that re-fires on change. A core sibling admitted by ADR 0028 (closes
// the Phase-B wave). Concrete sources (env, file), the merge+decode loader, and
// the cross-OS poll watcher live in internal/service/config; this package owns
// only the contract + the typed failure sentinels.
package config

// Source yields a configuration layer as a flat map (later sources override
// earlier ones in a Load). Implementations MUST be safe for concurrent Load.
type Source interface {
	// Load reads the source and returns its key→value map (nil map = empty layer).
	Load() (values map[string]any, err error)
}
