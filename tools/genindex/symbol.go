// Package main — one row of the symbol search index.
package main

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
