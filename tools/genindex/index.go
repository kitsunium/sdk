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

// symbol is one row in the search index.
//
// The field names are deliberately short: gzipped JSON is what ships to the
// browser, and the index is reloaded on every page navigation, so every byte is
// paid for once per page view rather than once per build. The type itself is
// unexported because nothing outside this command constructs one — only the
// JSON tags are part of any contract.
type symbol struct {
	// Kind is "func", "type", "const", "var" or "method".
	Kind string `json:"kind"`
	// Name is the bare identifier, e.g. "Marshal".
	Name string `json:"name"`
	// Qualified is the "pkg.Name" form, e.g. "codec.Marshal".
	Qualified string `json:"qualified"`
	// Package is the full import path.
	Package string `json:"package"`
	// PackageShort is the module-relative path, e.g. "codec".
	PackageShort string `json:"packageShort"`
	// Signature is the rendered Go signature, collapsed to a single line.
	Signature string `json:"signature"`
	// Doc is the synopsis — the first sentence, per godoc convention.
	Doc string `json:"doc,omitempty"`
	// URL is the anchor URL on the docs site.
	URL string `json:"url"`
	// Receiver names the receiver type; set only for methods.
	Receiver string `json:"receiver,omitempty"`
	// Examples are the names of the attached Example* functions.
	Examples []string `json:"examples,omitempty"`
	// Obsolete is true for an identifier whose doc opens a paragraph with the
	// godoc obsolescence marker. The JSON name stays "deprecated" because that
	// is what Search.astro reads.
	Obsolete bool `json:"deprecated,omitempty"`
	// SourceURL deep-links to the declaration on the source forge.
	SourceURL string `json:"sourceUrl,omitempty"`
}
