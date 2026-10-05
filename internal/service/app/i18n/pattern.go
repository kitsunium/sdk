package i18n

import (
	"strings"

	corei18n "github.com/kitsunium/sdk/internal/core/app/i18n"
)

// braces is the set the compiler scans for. Everything else in a pattern is
// literal, including "%", "$", "#" and every other character another
// templating syntax would have claimed.
const braces string = "{}"

// escapeLen is the width of a doubled brace: two bytes in, one byte out.
const escapeLen int = 2

// compilePattern parses text into a compiled [corei18n.PatternValue], or
// returns [corei18n.InvalidPattern].
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
//     numbers (see corei18n.Args); a specifier would announce that it does.
//   - Nested or select constructs — ICU MessageFormat's "{n, plural, one{…}}"
//     and "{gender, select, …}". Plural selection here is the corei18n.Form
//     the renderer picks from the language's own CLDR rules, so the message
//     body never has to encode a rule; and ICU MessageFormat is a parser
//     inside a translation file, which is a place nobody reviews a parser.
//   - Function or filter calls ("{name|upper}"). Case mapping is language
//     dependent — Turkish dotless i is the standing example — and this domain
//     ships no case mapper, so it must not appear to.
//   - Comments, whitespace trimming, and conditionals.
//
// A pattern is compiled ONCE, when the catalogue is built. A render never
// parses, which is both why a malformed translation fails at startup and why
// the hot path has nothing to fail at. The compiled value — and the grammar a
// placeholder NAME is held to, corei18n.ValidPlaceholder — are the core's;
// reading the text is this package's (ADR 0160: a wire format is a mechanism).
func compilePattern(text string) (compiled corei18n.PatternValue, err error) {
	//: a pattern with no brace at all is one literal span, and its text is
	//: the input string — shared, not copied.
	if !strings.ContainsAny(text, braces) {
		//: the empty pattern is legal and compiles to no spans at all.
		if text == "" {
			//: compiled, and empty — not the zero pattern.
			return corei18n.NewPattern(nil)
		}
		//: one span, zero allocations for its text.
		return corei18n.NewPattern([]corei18n.PartValue{{Text: text}})
	}
	//: the general path: escapes and placeholders.
	return compileBraced(text)
}

// compileBraced compiles a pattern that contains at least one brace.
func compileBraced(text string) (compiled corei18n.PatternValue, err error) {
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
			return corei18n.PatternValue{}, stepErr
		}
		//: past the construct.
		pos += advance
	}
	//: flush the trailing literal and hand back the compiled spans.
	return comp.done()
}
