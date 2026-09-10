// Package i18n — one CLDR category paired with the pattern that spells it.
package i18n

// formPattern pairs a CLDR category with the pattern registered for it.
//
// It is a slice element rather than a map entry because a message carries at
// most five of these: a linear scan over five values in one cache line beats a
// hash, and it allocates nothing on the render path.
type formPattern struct {
	// form is the category.
	form Form
	// body is the pattern for it.
	body pattern
}
