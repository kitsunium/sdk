// Package view — declares the sentinel *errs.Error outcomes of the domain: the
// port's, and the html/template engine's construction failures. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// No Public string in this package names a template, a path, a line, a data key
// or a fragment of template source. A Public is written to third parties, and
// everything a template engine handles is either the application's internal
// structure or somebody's data. What an operator needs travels in Fields and in
// Private; what a browser gets is one sentence with nothing in it.
package view

// exitConfig matches sysexits EX_CONFIG (78). A Renderer refused at
// construction is a permanent fault: the same tree will be refused forever, and
// the fix is an edit, never a retry.
const exitConfig int = 78

// exitSoftware matches sysexits EX_SOFTWARE (70) — the errs default, restated
// where a sentinel wants it explicitly.
const exitSoftware int = 70

// httpInternal is 500. Every render verdict in this domain is a server fault:
// the caller asked for a page, and which page failed is not something the
// browser gets to learn.
const httpInternal int = 500
