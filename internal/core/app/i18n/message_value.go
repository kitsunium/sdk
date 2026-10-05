package i18n

import "github.com/kitsunium/sdk/internal/kernel/errs"

// The two ASCII bounds that make a key unsafe to print.
const (
	// asciiSpace is the lowest printable ASCII byte; everything below it is a
	// C0 control character.
	asciiSpace byte = 0x20
	// asciiDelete is DEL, the one control character above the printable
	// range.
	asciiDelete byte = 0x7F
)

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

// NewCompiledMessage assembles a [MessageValue] from bodies already compiled,
// or returns [PluralFormMissing] or [InvalidForm].
//
// other is the [FormOther] body every message carries — a message whose
// other was never compiled is refused, because `other` is the category every
// language defines and the only one a message is required to carry. plural
// holds the remaining categories, ascending and distinct, and is empty for a
// message with no plural forms. plural is RETAINED, not copied, like the
// spans [NewPattern] keeps.
//
// It compiles nothing: reading a pattern's text is the compiler's, in
// internal/service/app/i18n, whose NewMessage and NewPluralMessage call this
// (ADR 0160: a wire format is a mechanism). And it does NOT check the
// categories against a language's rules — it does not know the language.
// Completeness against the rules of the language a catalogue registers the
// message under is checked by internal/service/app/i18n.NewStore, which does.
func NewCompiledMessage(other PatternValue, plural []FormPatternValue) (message MessageValue, err error) {
	//: the catch-all category is mandatory.
	if !other.set {
		//: refuse at construction rather than render a blank later.
		return MessageValue{}, errs.Wrap(PluralFormMissing, errs.WrapParams{},
			errs.String("form", FormOther.String()), errs.String("detail", "a message must carry the other form"))
	}
	//: every other category: a real one, in a fixed order, compiled.
	for index, entry := range plural {
		//: `other` is carried out of line, and a value outside the six cannot
		//: be asked for by any rule.
		if entry.Form == FormOther || !entry.Form.Valid() {
			//: refuse rather than store a category no rule can ask for.
			return MessageValue{}, errs.Wrap(InvalidForm, errs.WrapParams{}, errs.String("form", entry.Form.String()))
		}
		//: ascending and distinct, so two identical catalogues compile to
		//: identical messages and the scan in patternFor meets each once.
		if index > 0 && entry.Form <= plural[index-1].Form {
			//: the category is CLDR vocabulary, not data.
			return MessageValue{}, errs.Wrap(InvalidForm, errs.WrapParams{}, errs.String("form", entry.Form.String()),
				errs.String("detail", "the plural categories must be ascending and distinct"))
		}
		//: a category whose body was never compiled would render nothing.
		if !entry.Body.set {
			//: the category travels as a field.
			return MessageValue{}, errs.Wrap(PluralFormMissing, errs.WrapParams{}, errs.String("form", entry.Form.String()),
				errs.String("detail", "the category's pattern was never compiled"))
		}
	}
	//: nil, deliberately, for a message with only `other`: a nil slice is one
	//: word, an empty one is three.
	if len(plural) == 0 {
		//: the common case.
		return MessageValue{other: other}, nil
	}
	//: a compiled plural message.
	return MessageValue{other: other, plural: plural}, nil
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
func (m MessageValue) patternFor(form Form) (body PatternValue, ok bool) {
	//: the mandatory category is stored out of line, so it costs no scan.
	if form == FormOther {
		//: `set` is false for the zero message, which carries nothing.
		return m.other, m.other.set
	}
	//: at most five entries — a scan beats a map and allocates nothing.
	for _, candidate := range m.plural {
		//: exact category match.
		if candidate.Form == form {
			//: found.
			return candidate.Body, true
		}
	}
	//: the message does not carry this category.
	return PatternValue{}, false
}

// HasForm reports whether the message carries a pattern for form.
//
// It is what internal/service/app/i18n's load-time completeness check reads: for
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
