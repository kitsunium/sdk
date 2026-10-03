// Package i18n — one span of a compiled message pattern.
package i18n

// part is one span of a compiled pattern: literal text, or a placeholder name.
//
// The two are told apart by which field is EMPTY rather than by a kind tag,
// and that is safe rather than clever: the parser refuses "{}", so a
// placeholder name can never be empty, and a literal span never carries one.
// One less byte per span and one less branch per render.
type part struct {
	// text is the literal span. It is empty for a placeholder part.
	text string
	// name is the placeholder name. It is empty for a literal part.
	name string
}
