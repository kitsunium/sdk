// Package main — the extraction parameters shared by the whole walk.
package main

// indexOptions is everything the walk needs to turn a directory of Go files
// into index rows.
//
// They travel together because every stage needs the same set: dropping one at
// a call site is how a symbol ends up with a URL that points at the wrong site
// or a source link that points outside the repository.
type indexOptions struct {
	// root is the absolute module root the walk started from.
	root string
	// modulePath is the Go module path of root, used to build import paths.
	modulePath string
	// urlBase prefixes every symbol anchor on the docs site.
	urlBase string
	// repoRoot is the filesystem path the source links are made relative to.
	repoRoot string
	// sourceURLPrefix builds a forge deep-link when set; empty disables them.
	sourceURLPrefix string
}
