package i18n

import (
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// argSizeGuess is the number of bytes [MessageValue.Format] assumes a
// substituted value will occupy when it sizes the builder. It is a HINT: too
// small costs one growth, too large costs untouched capacity, and neither is a
// correctness question. Sixteen covers a formatted count, a short name and a
// filename stem; internal/service/app/i18n/BENCH.md reports the resulting
// allocations per render.
const argSizeGuess int = 16

// NewPattern assembles a compiled body from its spans, or returns
// [InvalidPattern].
//
// It parses nothing. Reading a pattern's TEXT — the braces, the "{{" and "}}"
// escapes, and every construct the syntax refuses by name — is the compiler's,
// in internal/service/app/i18n (ADR 0160: a wire format is a mechanism). What
// is checked here is what makes a list of spans a pattern at all: every span
// is literal text or a placeholder and never both, and every placeholder name
// is one [ValidPlaceholder] accepts, so a render never meets a name no caller
// could have supplied.
//
// parts is RETAINED, not copied: the caller hands it over and does not modify
// it afterwards. The compiler builds a fresh list per pattern, and a copy would
// cost an allocation per message at every catalogue load.
func NewPattern(parts []PartValue) (body PatternValue, err error) {
	//: the literal lengths, summed as the spans are checked.
	size := 0
	//: every span, in order, so the refusal names the first bad one.
	for index, span := range parts {
		//: a span is one of the two, never both and never neither.
		if (span.Text == "") == (span.Name == "") {
			//: the position is diagnostic; the span's text is developer data
			//: and is not repeated.
			return PatternValue{}, errs.Wrap(InvalidPattern, errs.WrapParams{}, errs.Int("span", index),
				errs.String("detail", "a span is literal text or a placeholder name, exactly one of the two"))
		}
		//: a placeholder name outside the grammar is a name no caller can supply.
		if span.Name != "" && !ValidPlaceholder(span.Name) {
			//: the NAME is developer-authored catalogue text and travels as a
			//: field; no Args value ever does.
			return PatternValue{}, errs.Wrap(InvalidPattern, errs.WrapParams{}, errs.Int("span", index), errs.String("placeholder", span.Name),
				errs.String("detail", "placeholder name is empty or outside [A-Za-z_][A-Za-z0-9_]*"))
		}
		//: count it toward the render builder's size.
		size += len(span.Text)
	}
	//: `set` marks it compiled, including when it has no spans at all.
	return PatternValue{parts: parts, size: size, set: true}, nil
}

// literal returns the pattern's text when it needs no substitution at all.
func (p PatternValue) literal() (text string, ok bool) {
	//: a compiled empty pattern renders as the empty string.
	if len(p.parts) == 0 {
		//: `set` distinguishes it from the zero pattern, which never reaches
		//: here because Format resolves that to PluralFormMissing first.
		return "", true
	}
	//: one literal span and nothing else — the common catalogue entry.
	if len(p.parts) == 1 && p.parts[0].Name == "" {
		//: the stored string IS the answer; no builder, no copy.
		return p.parts[0].Text, true
	}
	//: substitution is needed.
	return "", false
}

// expand renders a pattern that carries at least one placeholder.
func (p PatternValue) expand(args Args) (rendered string, err error) {
	//: one builder, sized from the literals plus a guess per substitution.
	var out strings.Builder
	//: the guess is a hint, never a correctness question — see argSizeGuess.
	out.Grow(p.size + argSizeGuess*(len(p.parts)-1))
	//: walk the compiled spans in order.
	for _, span := range p.parts {
		//: a literal span is copied verbatim.
		if span.Name == "" {
			//: no lookup, no escape.
			out.WriteString(span.Text)
			//: next span.
			continue
		}
		//: a placeholder must be supplied; an absent one is a hole.
		value, ok := args[span.Name]
		//: refuse rather than render "Welcome back, " and ship it.
		if !ok {
			//: the NAME travels as a field; no value does, here or anywhere.
			return "", errs.Wrap(ArgumentMissing, errs.WrapParams{}, errs.String("placeholder", span.Name))
		}
		//: substituted literally and never rescanned.
		out.WriteString(value)
	}
	//: the rendered message.
	return out.String(), nil
}

// ValidPlaceholder reports whether name matches [A-Za-z_][A-Za-z0-9_]*.
//
// The grammar is narrow on purpose. It is the set of names that can be written
// in every catalogue format the codec domain speaks without quoting, that can
// appear in an error field without escaping, and that cannot be confused with
// a format specifier — so a translator who writes "{count:02d}" is told the
// name is invalid rather than handed a placeholder literally called
// "count:02d" that no caller will ever supply. It is the compiler's test for a
// name, and [NewPattern]'s for a span.
func ValidPlaceholder(name string) bool {
	//: "{}" carries no name, and a name may not begin with a digit — so a
	//: placeholder is never a position.
	if name == "" || !isNameStart(name[0]) {
		//: refuse.
		return false
	}
	//: the remainder admits digits as well.
	for i := 1; i < len(name); i++ {
		//: letters, digits and underscore.
		if !isNameStart(name[i]) && (name[i] < '0' || name[i] > '9') {
			//: refuse.
			return false
		}
	}
	//: a usable name.
	return true
}

// isNameStart reports whether b may begin a placeholder name.
func isNameStart(b byte) bool {
	//: ASCII letters and underscore.
	return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}
