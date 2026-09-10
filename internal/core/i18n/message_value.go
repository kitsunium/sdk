// Package i18n — the message key, the compiled message, and its rendering.
package i18n

import (
	"maps"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The two ASCII bounds that make a key unsafe to print.
const (
	// asciiSpace is the lowest printable ASCII byte; everything below it is a
	// C0 control character.
	asciiSpace byte = 0x20
	// asciiDelete is DEL, the one control character above the printable
	// range.
	asciiDelete byte = 0x7F
)

// Key names a message inside a catalogue. It is a program identifier the
// developer chooses — "checkout.button.pay" — and never prose: it is compared
// by byte equality, it travels in error fields, and it is what the renderer
// puts on screen when the translation is missing. That last use is why an
// empty key is refused: a blank on screen is a defect nobody reports.
type Key string

// ValidateKey reports whether key is usable, and returns [InvalidKey] when it
// is not.
//
// The rule is deliberately thin — non-empty, no ASCII control characters — and
// it does not impose a dotted grammar, a namespace or a case convention.
// Those are the application's spelling, and an SDK that imposed one would make
// half the catalogues in existence unloadable to buy nothing. What it does
// close is the pair of characters that make a key unsafe to SHOW: a newline
// splits a log line in two, and a terminal escape rewrites the line above it.
func ValidateKey(key Key) error {
	//: an empty key renders as a blank, which is the failure mode the whole
	//: missing-translation policy exists to avoid.
	if key == "" {
		//: refuse at construction.
		return errs.Wrap(InvalidKey, errs.WrapParams{}, errs.String("detail", "empty"))
	}
	//: the key is reported in a field when it is refused; converting once
	//: keeps the loop free of a per-iteration conversion.
	text := string(key)
	//: control characters make the key unsafe to print, and it gets printed.
	for i := range len(text) {
		//: ASCII C0 plus DEL.
		if text[i] < asciiSpace || text[i] == asciiDelete {
			//: the key itself is developer-authored and travels as a field.
			return errs.Wrap(InvalidKey, errs.WrapParams{}, errs.String("key", text), errs.String("detail", "control character"))
		}
	}
	//: usable.
	return nil
}

// MessageValue is one translated message, compiled: its placeholders are
// parsed once, when the catalogue is built, so a malformed pattern fails at
// startup and a render never parses anything. pkg/v1/i18n publishes it as
// `Message`.
//
// A MessageValue ALWAYS carries the [FormOther] pattern — the one category
// CLDR guarantees in every language — and carries the other five only when the
// translator supplied them. The zero MessageValue carries nothing and reports
// [PluralFormMissing] from [MessageValue.Format]; it is not an empty
// translation.
//
// A MessageValue does not know which language it is written in. That is
// deliberate: the renderer walks the fallback chain and therefore already
// knows which language answered, and storing the tag here would put a second
// copy of that fact where nothing checks it against the first. See the package
// comment on why the plural rules follow the message.
type MessageValue struct {
	// other is the mandatory FormOther pattern.
	other pattern
	// plural holds the remaining categories, ascending by Form. It is nil for
	// a message with no plural forms, which is the common case and the one
	// worth keeping small.
	plural []formPattern
}

// NewMessage compiles a message with no plural forms, or returns
// [InvalidPattern].
//
// The pattern is text with named placeholders: "Welcome back, {name}". A
// literal brace is written "{{" or "}}". Everything else about the syntax is
// refused BY NAME — see [compilePattern].
func NewMessage(text string) (message MessageValue, err error) {
	//: compile once, here, so no render ever parses.
	body, err := compilePattern(text)
	//: a malformed pattern is a catalogue defect, refused at load.
	if err != nil {
		//: InvalidPattern, already carrying the position.
		return MessageValue{}, err
	}
	//: a message with no plural forms carries only FormOther.
	return MessageValue{other: body}, nil
}

// NewPluralMessage compiles a message with one pattern per CLDR category, or
// returns [InvalidPattern] or [PluralFormMissing].
//
// forms MUST contain [FormOther]: it is the category every language defines
// and the only one a message is required to carry. A map without it is
// refused here rather than at render time, because at render time the caller
// is a request and the only available repair is to show something wrong.
//
// This constructor does NOT check the map against a language's rules — it does
// not know the language. Completeness against the rules of the language a
// catalogue registers the message under is checked by
// internal/service/i18n.NewStore, which does. Both checks are at load time;
// neither is at render time.
func NewPluralMessage(forms map[Form]string) (message MessageValue, err error) {
	//: the catch-all category is mandatory.
	text, ok := forms[FormOther]
	//: a plural message without `other` has no pattern for the quantities no
	//: other clause matches, which in most languages is nearly all of them.
	if !ok {
		//: refuse at construction.
		return MessageValue{}, errs.Wrap(PluralFormMissing, errs.WrapParams{},
			errs.String("form", FormOther.String()), errs.String("detail", "a plural message must carry the other form"))
	}
	//: compile the mandatory pattern first, so its position is reported first.
	other, err := compilePattern(text)
	//: a malformed `other` pattern stops the compile.
	if err != nil {
		//: InvalidPattern.
		return MessageValue{}, err
	}
	//: compile the remaining categories in a deterministic order.
	plural, err := compileForms(forms)
	//: a malformed sibling pattern stops it too.
	if err != nil {
		//: InvalidPattern or InvalidForm.
		return MessageValue{}, err
	}
	//: a compiled plural message.
	return MessageValue{other: other, plural: plural}, nil
}

// compileForms compiles every category in forms except [FormOther], sorted
// ascending so the stored order does not depend on Go's map iteration and two
// identical catalogues compile to identical messages.
func compileForms(forms map[Form]string) (compiled []formPattern, err error) {
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
func compileSorted(kinds []Form, forms map[Form]string) (compiled []formPattern, err error) {
	//: at most one entry per category, minus the mandatory one.
	compiled = make([]formPattern, 0, len(kinds)-1)
	//: in the fixed order.
	for _, kind := range kinds {
		//: `other` is already compiled by the caller.
		if kind == FormOther {
			//: skip.
			continue
		}
		//: an out-of-range Form cannot be honoured.
		if !kind.Valid() {
			//: refuse rather than store a category no rule can ask for.
			return nil, errs.Wrap(InvalidForm, errs.WrapParams{}, errs.String("form", kind.String()))
		}
		//: compile this category's pattern.
		body, compileErr := compilePattern(forms[kind])
		//: a malformed pattern is a catalogue defect.
		if compileErr != nil {
			//: InvalidPattern, carrying the position.
			return nil, compileErr
		}
		//: store it.
		compiled = append(compiled, formPattern{form: kind, body: body})
	}
	//: the compiled categories.
	return compiled, nil
}

// Format renders the pattern registered for form, substituting args.
//
// It returns [PluralFormMissing] when the message does not carry form, and
// [ArgumentMissing] when a placeholder the pattern names is absent from args.
// Neither error names an argument value.
//
// A SURPLUS argument is ignored, deliberately and asymmetrically. One Args map
// is handed to every language in turn, and languages legitimately use
// different subsets of the placeholders a message offers: French may need a
// gender argument English does not, and refusing the extra would make the
// English render fail for a reason that lives in the French catalogue. A
// MISSING argument is the opposite — it is a hole in the sentence being
// rendered right now — so it is an error.
//
// Substitution is literal and single-pass: a value containing "{name}"
// produces those six characters and is never rescanned. Nothing is escaped
// here; see [Args].
func (m MessageValue) Format(form Form, args Args) (rendered string, err error) {
	//: resolve the category to its compiled pattern.
	body, ok := m.patternFor(form)
	//: a category the message does not carry is a refusal, never a silent
	//: substitution of `other` — that renders a grammatically wrong sentence.
	if !ok {
		//: the category travels as a field; it is CLDR vocabulary, not data.
		return "", errs.Wrap(PluralFormMissing, errs.WrapParams{}, errs.String("form", form.String()))
	}
	//: an empty pattern and a single literal both answer without a builder.
	if literal, direct := body.literal(); direct {
		//: zero allocations on the most common shape in any catalogue.
		return literal, nil
	}
	//: the general path: literals interleaved with substitutions.
	return body.expand(args)
}

// patternFor resolves a category to its compiled body.
func (m MessageValue) patternFor(form Form) (body pattern, ok bool) {
	//: the mandatory category is stored out of line, so it costs no scan.
	if form == FormOther {
		//: `set` is false for the zero message, which carries nothing.
		return m.other, m.other.set
	}
	//: at most five entries — a scan beats a map and allocates nothing.
	for _, candidate := range m.plural {
		//: exact category match.
		if candidate.form == form {
			//: found.
			return candidate.body, true
		}
	}
	//: the message does not carry this category.
	return pattern{}, false
}

// HasForm reports whether the message carries a pattern for form.
//
// It is what internal/service/i18n's load-time completeness check reads: for
// each category the registered language's rules can produce, the message must
// answer true, or the catalogue is refused naming the key and the category.
func (m MessageValue) HasForm(form Form) bool {
	//: the same resolution Format performs, without the render.
	_, ok := m.patternFor(form)
	//: present or not.
	return ok
}

// IsPlural reports whether the message carries any category beyond
// [FormOther].
func (m MessageValue) IsPlural() bool {
	//: the plural slice is nil for a message with only `other`.
	return len(m.plural) > 0
}
