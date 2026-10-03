// Package i18n — the placeholder parser, run once per catalogue entry.
package i18n

import (
	"strings"

	corei18n "github.com/kitsunium/sdk/internal/core/app/i18n"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// patternCompiler accumulates the spans of one pattern. It exists so the
// literal run and the placeholder list are advanced by named steps rather than
// by a closure capturing three variables.
type patternCompiler struct {
	// parts is the span list being built.
	parts []corei18n.PartValue
	// lit accumulates the literal run that has not been flushed yet.
	lit strings.Builder
}

// step consumes the brace construct starting at pos and reports how many bytes
// it occupied.
func (c *patternCompiler) step(text string, pos int) (advance int, err error) {
	//: a closing brace is only ever legal as the escape "}}".
	if text[pos] == '}' {
		//: doubled means a literal "}".
		if pos+1 < len(text) && text[pos+1] == '}' {
			//: one literal byte, two consumed.
			c.lit.WriteByte('}')
			//: consumed.
			return escapeLen, nil
		}
		//: a lone "}" is refused rather than passed through, because passing
		//: it through makes "{name}" and "name}" both render, and only one of
		//: them was meant.
		return 0, errs.Wrap(corei18n.InvalidPattern, errs.WrapParams{}, errs.Int("offset", pos), errs.String("detail", "unmatched closing brace"))
	}
	//: a doubled opening brace is a literal "{".
	if pos+1 < len(text) && text[pos+1] == '{' {
		//: one literal byte, two consumed.
		c.lit.WriteByte('{')
		//: consumed.
		return escapeLen, nil
	}
	//: otherwise it opens a placeholder.
	return c.placeholder(text, pos)
}

// placeholder consumes "{name}" at pos.
func (c *patternCompiler) placeholder(text string, pos int) (advance int, err error) {
	//: find the terminator.
	end := strings.IndexByte(text[pos+1:], '}')
	//: an unclosed placeholder swallows the rest of the sentence.
	if end < 0 {
		//: refuse at load, where the catalogue file is in front of someone.
		return 0, errs.Wrap(corei18n.InvalidPattern, errs.WrapParams{}, errs.Int("offset", pos), errs.String("detail", "unclosed placeholder"))
	}
	//: the name between the braces.
	name := text[pos+1 : pos+1+end]
	//: the name grammar is checked here, once, not at every render.
	if !corei18n.ValidPlaceholder(name) {
		//: the NAME is developer-authored catalogue text and travels as a
		//: field; no Args value ever does.
		return 0, errs.Wrap(corei18n.InvalidPattern, errs.WrapParams{}, errs.Int("offset", pos), errs.String("placeholder", name),
			errs.String("detail", "placeholder name is empty or outside [A-Za-z_][A-Za-z0-9_]*"))
	}
	//: the literal run ends where the placeholder begins.
	c.flush()
	//: record the substitution point.
	c.parts = append(c.parts, corei18n.PartValue{Name: name})
	//: "{" + name + "}".
	return end + escapeLen, nil
}

// flush moves the accumulated literal run into the span list.
func (c *patternCompiler) flush() {
	//: two adjacent placeholders produce no literal between them.
	if c.lit.Len() == 0 {
		//: nothing to flush.
		return
	}
	//: one span, one string.
	c.parts = append(c.parts, corei18n.PartValue{Text: c.lit.String()})
	//: start the next run.
	c.lit.Reset()
}

// done flushes the trailing literal and returns the compiled pattern.
func (c *patternCompiler) done() (compiled corei18n.PatternValue, err error) {
	//: the tail of the pattern.
	c.flush()
	//: the spans become a value; the core holds them to its invariants.
	return corei18n.NewPattern(c.parts)
}
