// Package main — the top-level search-index document.
package main

// schemaVersion bumps when the JSON shape changes incompatibly.
//
// Search.astro pins to a known schema so an upgrade skew surfaces at build time
// instead of silently breaking the search UI — which would otherwise degrade to
// an empty result set that looks exactly like "nothing matched".
const schemaVersion int = 1

// index is the top-level JSON document the docs site loads.
//
// GeneratedAt is stamped rather than derived so a stale index is identifiable
// from the file alone, without having to diff it against the tree it was built
// from.
type index struct {
	// Schema is the shape version Search.astro pins to.
	Schema int `json:"schema"`
	// GeneratedAt is the RFC 3339 build timestamp, in UTC.
	GeneratedAt string `json:"generatedAt"`
	// Module is the Go module path the symbols were extracted from.
	Module string `json:"module"`
	// Symbols is every exported declaration found under the module root.
	Symbols []symbol `json:"symbols"`
}
