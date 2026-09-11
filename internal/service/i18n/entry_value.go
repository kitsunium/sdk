// Package i18n — a catalogue entry, in the two shapes a catalogue file has.
package i18n

import corei18n "github.com/kitsunium/sdk/internal/core/i18n"

// EntryValue is one catalogue entry before it is compiled: either a plain
// pattern, or one pattern per CLDR category. pkg/v1/i18n publishes it as
// `Entry`.
//
// The two shapes are what a catalogue FILE carries — a string, or a map from
// category name to string — so this type is the file's shape given a name, and
// the decode from any of the codec domain's formats lands on it without a
// format-specific struct tag anywhere.
//
// Which shape was used is not cosmetic: a Forms entry DECLARES the message is
// counted, and a counted message is checked at load against every category its
// language can produce. An entry written as a plain string is not counted and
// is not checked, because "Welcome back" needs no plural form in any language.
type EntryValue struct {
	// Pattern is the message body when the entry is not counted. It is
	// ignored when Forms is non-nil.
	Pattern string
	// Forms maps CLDR category names — "zero", "one", "two", "few", "many",
	// "other" — to patterns. A non-nil Forms marks the entry counted, even
	// when it holds a single category.
	Forms map[string]string
}

// Catalogue is one language's entries, keyed by message key.
type Catalogue map[corei18n.Key]EntryValue

// Plain returns an uncounted [EntryValue].
func Plain(text string) EntryValue {
	//: the body, and no declared categories.
	return EntryValue{Pattern: text}
}

// PluralForms returns a counted [EntryValue] whose patterns are keyed by CLDR
// category name.
//
// A non-nil, EMPTY map still marks the entry counted, and is refused at load
// for missing `other` — which is the honest answer to a translator who wrote
// `"cart.items": {}`.
func PluralForms(forms map[string]string) EntryValue {
	//: an explicit non-nil map, so an empty one still declares a counted
	//: message rather than collapsing into an uncounted empty string.
	if forms == nil {
		//: preserve the declaration.
		forms = map[string]string{}
	}
	//: the declared categories.
	return EntryValue{Forms: forms}
}
