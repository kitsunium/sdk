// Package i18n — the placeholder syntax, compiled once at catalogue load.
package i18n

import (
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// braces is the set the compiler scans for. Everything else in a pattern is
// literal, including "%", "$", "#" and every other character another
// templating syntax would have claimed.
const braces string = "{}"

// escapeLen is the width of a doubled brace: two bytes in, one byte out.
const escapeLen int = 2

// argSizeGuess is the number of bytes [MessageValue.Format] assumes a
// substituted value will occupy when it sizes the builder. It is a HINT: too
// small costs one growth, too large costs untouched capacity, and neither is a
// correctness question. Sixteen covers a formatted count, a short name and a
// filename stem; BENCH.md reports the resulting allocations per render.
const argSizeGuess int = 16

// pattern is one compiled message body.
type pattern struct {
	// parts is the compiled span list, in order.
	parts []part
	// size is the sum of the literal lengths, used to size the builder.
	size int
	// set distinguishes a successfully compiled EMPTY pattern from a pattern
	// that was never compiled at all. Without it the zero MessageValue would
	// render as "" and look like a translation.
	set bool
}

// compilePattern parses text into a compiled [pattern], or returns
// [InvalidPattern].
//
// # The syntax, and everything it deliberately is not
//
// A pattern is literal text with named placeholders: "You have {count} new
// {kind} messages". A literal brace is written "{{" or "}}". That is the
// entire grammar.
//
// Refused BY NAME, each because the alternative is a language:
//
//   - Positional placeholders ("{0}", "%s", "%1$s"). A translator reorders a
//     sentence — that is most of the job — and a positional argument that
//     moves changes meaning silently. A name cannot.
//   - Format specifiers ("{count:03d}", "%.2f"). This domain does not format
//     numbers (see [Args]); a specifier would announce that it does.
//   - Nested or select constructs — ICU MessageFormat's "{n, plural, one{…}}"
//     and "{gender, select, …}". Plural selection here is the [Form] the
//     renderer picks from the language's own CLDR rules, so the message body
//     never has to encode a rule; and ICU MessageFormat is a parser inside a
//     translation file, which is a place nobody reviews a parser.
//   - Function or filter calls ("{name|upper}"). Case mapping is language
//     dependent — Turkish dotless i is the standing example — and this domain
//     ships no case mapper, so it must not appear to.
//   - Comments, whitespace trimming, and conditionals.
//
// A pattern is compiled ONCE, when the catalogue is built. A render never
// parses, which is both why a malformed translation fails at startup and why
// the hot path has nothing to fail at.
func compilePattern(text string) (compiled pattern, err error) {
	//: a pattern with no brace at all is one literal span, and its text is
	//: the input string — shared, not copied.
	if !strings.ContainsAny(text, braces) {
		//: the empty pattern is legal and compiles to no spans at all.
		if text == "" {
			//: `set` is what distinguishes it from the zero pattern.
			return pattern{set: true}, nil
		}
		//: one span, zero allocations for its text.
		return pattern{parts: []part{{text: text}}, size: len(text), set: true}, nil
	}
	//: the general path: escapes and placeholders.
	return compileBraced(text)
}

// compileBraced compiles a pattern that contains at least one brace.
func compileBraced(text string) (compiled pattern, err error) {
	//: one accumulator for the whole pattern.
	var comp patternCompiler
	//: cursor into text.
	pos := 0
	//: jump from brace to brace rather than inspecting every byte.
	for pos < len(text) {
		//: the next brace, or the end.
		next := strings.IndexAny(text[pos:], braces)
		//: no further brace — the rest is literal.
		if next < 0 {
			//: copy the tail and stop.
			comp.lit.WriteString(text[pos:])
			//: done.
			break
		}
		//: the literal run before the brace.
		comp.lit.WriteString(text[pos : pos+next])
		//: advance to the brace itself.
		pos += next
		//: consume the brace construct, whatever it turns out to be.
		advance, stepErr := comp.step(text, pos)
		//: a malformed construct is a catalogue defect.
		if stepErr != nil {
			//: InvalidPattern, carrying the offset.
			return pattern{}, stepErr
		}
		//: past the construct.
		pos += advance
	}
	//: flush the trailing literal and hand back the compiled spans.
	return comp.done(), nil
}

// literal returns the pattern's text when it needs no substitution at all.
func (p pattern) literal() (text string, ok bool) {
	//: a compiled empty pattern renders as the empty string.
	if len(p.parts) == 0 {
		//: `set` distinguishes it from the zero pattern, which never reaches
		//: here because Format resolves that to PluralFormMissing first.
		return "", true
	}
	//: one literal span and nothing else — the common catalogue entry.
	if len(p.parts) == 1 && p.parts[0].name == "" {
		//: the stored string IS the answer; no builder, no copy.
		return p.parts[0].text, true
	}
	//: substitution is needed.
	return "", false
}

// expand renders a pattern that carries at least one placeholder.
func (p pattern) expand(args Args) (rendered string, err error) {
	//: one builder, sized from the literals plus a guess per substitution.
	var out strings.Builder
	//: the guess is a hint, never a correctness question — see argSizeGuess.
	out.Grow(p.size + argSizeGuess*(len(p.parts)-1))
	//: walk the compiled spans in order.
	for _, span := range p.parts {
		//: a literal span is copied verbatim.
		if span.name == "" {
			//: no lookup, no escape.
			out.WriteString(span.text)
			//: next span.
			continue
		}
		//: a placeholder must be supplied; an absent one is a hole.
		value, ok := args[span.name]
		//: refuse rather than render "Welcome back, " and ship it.
		if !ok {
			//: the NAME travels as a field; no value does, here or anywhere.
			return "", errs.Wrap(ArgumentMissing, errs.WrapParams{}, errs.String("placeholder", span.name))
		}
		//: substituted literally and never rescanned.
		out.WriteString(value)
	}
	//: the rendered message.
	return out.String(), nil
}

// validPlaceholder reports whether name matches [A-Za-z_][A-Za-z0-9_]*.
//
// The grammar is narrow on purpose. It is the set of names that can be written
// in every catalogue format the codec domain speaks without quoting, that can
// appear in an error field without escaping, and that cannot be confused with
// a format specifier — so a translator who writes "{count:02d}" is told the
// name is invalid rather than handed a placeholder literally called
// "count:02d" that no caller will ever supply.
func validPlaceholder(name string) bool {
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
