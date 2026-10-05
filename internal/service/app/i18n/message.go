package i18n

import (
	"maps"
	"slices"

	corei18n "github.com/kitsunium/sdk/internal/core/app/i18n"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// NewMessage compiles a message with no plural forms, or returns
// [corei18n.InvalidPattern].
//
// The pattern is text with named placeholders: "Welcome back, {name}". A
// literal brace is written "{{" or "}}". Everything else about the syntax is
// refused BY NAME — see compilePattern.
func NewMessage(text string) (message corei18n.MessageValue, err error) {
	//: compile once, here, so no render ever parses.
	body, err := compilePattern(text)
	//: a malformed pattern is a catalogue defect, refused at load.
	if err != nil {
		//: InvalidPattern, already carrying the position.
		return corei18n.MessageValue{}, err
	}
	//: a message with no plural forms carries only FormOther.
	return corei18n.NewCompiledMessage(body, nil)
}

// NewPluralMessage compiles a message with one pattern per CLDR category, or
// returns [corei18n.InvalidPattern], [corei18n.InvalidForm] or
// [corei18n.PluralFormMissing].
//
// forms MUST contain [corei18n.FormOther]: it is the category every language
// defines and the only one a message is required to carry. A map without it
// is refused here rather than at render time, because at render time the
// caller is a request and the only available repair is to show something
// wrong.
//
// This constructor does NOT check the map against a language's rules — it does
// not know the language. Completeness against the rules of the language a
// catalogue registers the message under is checked by [NewStore], which does.
// Both checks are at load time; neither is at render time.
func NewPluralMessage(forms map[corei18n.Form]string) (message corei18n.MessageValue, err error) {
	//: the catch-all category is mandatory.
	text, ok := forms[corei18n.FormOther]
	//: a plural message without `other` has no pattern for the quantities no
	//: other clause matches, which in most languages is nearly all of them.
	if !ok {
		//: refuse at construction.
		return corei18n.MessageValue{}, errs.Wrap(corei18n.PluralFormMissing, errs.WrapParams{},
			errs.String("form", corei18n.FormOther.String()), errs.String("detail", "a plural message must carry the other form"))
	}
	//: compile the mandatory pattern first, so its position is reported first.
	other, err := compilePattern(text)
	//: a malformed `other` pattern stops the compile.
	if err != nil {
		//: InvalidPattern.
		return corei18n.MessageValue{}, err
	}
	//: compile the remaining categories in a deterministic order.
	plural, err := compileForms(forms)
	//: a malformed sibling pattern stops it too.
	if err != nil {
		//: InvalidPattern or InvalidForm.
		return corei18n.MessageValue{}, err
	}
	//: a compiled plural message.
	return corei18n.NewCompiledMessage(other, plural)
}

// compileForms compiles every category in forms except corei18n.FormOther,
// sorted ascending so the stored order does not depend on Go's map iteration
// and two identical catalogues compile to identical messages.
func compileForms(forms map[corei18n.Form]string) (compiled []corei18n.FormPatternValue, err error) {
	//: nothing but `other` means no plural slice at all — the common case.
	if len(forms) <= 1 {
		//: nil, deliberately: a nil slice is one word, an empty one is three.
		return nil, nil
	}
	//: a stable, value-ordered layout, independent of map iteration.
	kinds := slices.Sorted(maps.Keys(forms))
	//: compile in that order.
	return compileSorted(kinds, forms)
}

// compileSorted compiles the already-ordered categories, skipping the
// mandatory one its caller has already compiled.
func compileSorted(kinds []corei18n.Form, forms map[corei18n.Form]string) (compiled []corei18n.FormPatternValue, err error) {
	//: at most one entry per category, minus the mandatory one.
	compiled = make([]corei18n.FormPatternValue, 0, len(kinds)-1)
	//: in the fixed order.
	for _, kind := range kinds {
		//: `other` is already compiled by the caller.
		if kind == corei18n.FormOther {
			//: skip.
			continue
		}
		//: an out-of-range Form cannot be honoured.
		if !kind.Valid() {
			//: refuse rather than store a category no rule can ask for.
			return nil, errs.Wrap(corei18n.InvalidForm, errs.WrapParams{}, errs.String("form", kind.String()))
		}
		//: compile this category's pattern.
		body, compileErr := compilePattern(forms[kind])
		//: a malformed pattern is a catalogue defect.
		if compileErr != nil {
			//: InvalidPattern, carrying the position.
			return nil, compileErr
		}
		//: store it.
		compiled = append(compiled, corei18n.FormPatternValue{Form: kind, Body: body})
	}
	//: the compiled categories.
	return compiled, nil
}
