// Package main — the per-file context every rule reads.
package main

import "go/token"

// fileCtx carries everything a rule needs about one parsed file: where it came
// from, which packages it imports under which local names, and which lines
// carry an exemption.
type fileCtx struct {
	// fset resolves ast positions to file:line:col.
	fset *token.FileSet
	// byPath maps an import path to the local name it is bound to in this
	// file. A rule matches selectors against the local name, so an aliased or
	// renamed import is caught exactly like a plain one.
	byPath map[string]string
	// dotImports maps a dot-imported path to the position of its import spec.
	//
	// A dot import binds no qualifier, so calls appear unqualified and no
	// selector match is possible: `. "log/slog"` turns slog.New into New, and
	// every selector-based rule goes silent on a file that may well be
	// building a second pipeline. Recording it lets the rules say so instead
	// of passing quietly, which is the only honest option without type
	// resolution.
	dotImports map[string]token.Pos
	// suppressed records, per line, which rule IDs an inline directive exempts.
	suppressed map[int]map[string]bool
	// bridgeBound names the identifiers this file assigns from a slog-bridge
	// constructor, so a handler bound to a variable is still recognised as
	// the sanctioned composition when it reaches slog.New.
	bridgeBound map[string]bool
	// shadowed names the import local names this file also declares as an
	// identifier. Without type resolution a selector cannot be told apart
	// from a field access on a same-named local, so those packages stop
	// matching here — see shadowedNames for why that direction.
	shadowed map[string]bool
}
