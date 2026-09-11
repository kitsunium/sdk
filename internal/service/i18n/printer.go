// Package i18n — the renderer: one language, one resolution chain.
package i18n

import (
	"strings"

	corei18n "github.com/kitsunium/sdk/internal/core/i18n"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// chainSeparator joins the languages a failed lookup tried, for the operator's
// field.
const chainSeparator string = " "

// Printer renders messages in one language.
//
// It holds a resolution CHAIN — the requested tag, its parents, then the
// catalogue's fallback and its parents — and each step carries its own plural
// rules. A render walks the chain, takes the first language that holds the
// key, and selects the plural form with that language's rules.
//
// # Build one per language, not one per request
//
// [NewPrinter] resolves the chain and allocates; [Printer.Render] walks it and
// does not. A server negotiates a language per request and then wants a
// Printer for it, so the shape that pays off is a map from tag to Printer
// built at startup:
//
//	printers := map[i18n.Tag]*i18n.Printer{}
//	for _, tag := range store.Tags() {
//		p, err := i18n.NewPrinter(store, tag)
//		…
//		printers[tag] = p
//	}
//	// per request
//	p := printers[negotiator.Negotiate(r.Header.Get("Accept-Language"))]
//
// BENCH.md reports both costs so the difference is a number rather than
// advice.
//
// A Printer is immutable and safe for concurrent use.
type Printer struct {
	// catalog is the message source. It is the port, not the concrete Store,
	// so a caller can render from anything that implements it.
	catalog corei18n.Catalog
	// tag is the language that was requested — what a caller puts in
	// Content-Language or an html lang attribute.
	tag corei18n.TagValue
	// chain is the resolution order, most specific first.
	chain []link
}

// NewPrinter returns a [Printer] rendering tag out of catalog, or refuses.
//
// Refused: a zero tag ([CatalogInvalid]) and a language with no reviewed CLDR
// rule ([UnsupportedLanguage]). Both are wiring faults — a request never
// reaches here with an arbitrary tag, because negotiation returns one of the
// supported set — so refusing at construction costs a startup failure and buys
// a render path with no language it cannot plural.
//
// The chain is the requested tag, then its parents ("fr-CA" → "fr"), then —
// when catalog implements [corei18n.Fallbacker] — the fallback and its
// parents. A catalog with no fallback simply has a shorter chain, which is
// ADR 0039's absence-is-the-answer shape rather than a nil tag to interpret.
func NewPrinter(catalog corei18n.Catalog, tag corei18n.TagValue) (printer *Printer, err error) {
	//: a renderer with no language cannot be built.
	if tag.IsZero() {
		//: ADR 0031: refuse rather than default to a language the SDK picked.
		return nil, errs.Wrap(CatalogInvalid, errs.WrapParams{}, errs.String("detail", "the printer tag is unset"))
	}
	//: build the chain, refusing any step whose language has no rules.
	chain, err := buildChain(catalog, tag)
	//: UnsupportedLanguage.
	if err != nil {
		//: refuse.
		return nil, err
	}
	//: an immutable renderer.
	return &Printer{catalog: catalog, tag: tag, chain: chain}, nil
}

// Tag returns the language this printer was built for — the value a caller
// puts in Content-Language or an html lang attribute.
//
// It is the language REQUESTED, not the one that answered a particular render.
// The two differ exactly when a key was missing and the chain fell back, and
// the SDK deliberately does not report that per call: see [Printer.Render] and
// [Store.Missing].
func (p *Printer) Tag() corei18n.TagValue {
	//: fixed at construction.
	return p.tag
}

// Render renders the uncounted message registered under key.
//
// # A missing key returns the KEY, and an error
//
// The string is never empty. A caller that checks the error fails the render;
// a caller that ignores it ships "checkout.button.pay" to the screen, which is
// visible, greppable, and gets reported within the hour. The alternative — an
// empty string — is a blank space in a layout that still looks finished, and
// it is the failure mode this domain most wants to make impossible to ship
// quietly. See ADR 0063 §D4.
//
// The key is a developer identifier, never user data, so putting it on screen
// discloses nothing an attacker could not read in the JavaScript bundle.
//
// # What it does not tell you
//
// It does not report which language actually answered. When a key is present
// in the fallback and absent from the requested language, the render succeeds
// with the fallback's text and no signal. That is deliberate: a per-call
// signal would be checked by nobody and would put a branch on the hot path,
// while the same question is answered exhaustively and at build time by
// [Store.Missing]. A caller who needs Content-Language to be exact asserts
// there that the gap is empty.
func (p *Printer) Render(key corei18n.Key, args corei18n.Args) (rendered string, err error) {
	//: an uncounted message has only the category every message carries.
	message, _, ok := p.resolve(key)
	//: a key no language in the chain holds.
	if !ok {
		//: the key itself, and the typed refusal beside it.
		return string(key), p.notFound(key)
	}
	//: substitute.
	return message.Format(corei18n.FormOther, args)
}

// RenderCount renders the counted message registered under key, selecting the
// CLDR plural form for count.
//
// The form is selected with the plural rules of the language that ANSWERED,
// not of the language that was requested. When the requested language does not
// hold the key and the fallback does, the fallback's rules apply — because the
// fallback's message is the one being rendered and it carries the fallback's
// categories. Selecting with the requested language's rules would ask an
// English message for `few`. See ADR 0063 §D3.
//
// A missing key behaves exactly as in [Printer.Render].
func (p *Printer) RenderCount(key corei18n.Key, count corei18n.CountValue, args corei18n.Args) (rendered string, err error) {
	//: resolve the message AND the rules of the language that holds it.
	message, rules, ok := p.resolve(key)
	//: a key no language in the chain holds.
	if !ok {
		//: the key itself, and the typed refusal beside it.
		return string(key), p.notFound(key)
	}
	//: the category, chosen by the answering language's own rules.
	form := rules.Select(count)
	//: substitute.
	return message.Format(form, args)
}

// resolve walks the chain and returns the first message found together with
// the plural rules of the language that held it.
func (p *Printer) resolve(key corei18n.Key) (message corei18n.MessageValue, rules PluralValue, ok bool) {
	//: most specific first.
	for _, step := range p.chain {
		//: exact lookup; the port does no walking of its own.
		found, hit := p.catalog.Lookup(step.tag, key)
		//: not in this language.
		if !hit {
			//: try the next step.
			continue
		}
		//: the message, and the rules that go with it.
		return found, step.rules, true
	}
	//: nothing in the chain holds it.
	return corei18n.MessageValue{}, PluralValue{}, false
}

// notFound builds the [corei18n.MessageNotFound] refusal, naming the key and
// the languages that were tried.
func (p *Printer) notFound(key corei18n.Key) error {
	//: the chain as a readable list, for the operator.
	var tried strings.Builder
	//: space separated, in resolution order.
	for i, step := range p.chain {
		//: no leading separator.
		if i > 0 {
			//: separator.
			tried.WriteString(chainSeparator)
		}
		//: the canonical spelling.
		tried.WriteString(step.tag.String())
	}
	//: the key and the chain travel as fields; no Args value ever does.
	return errs.Wrap(corei18n.MessageNotFound, errs.WrapParams{},
		errs.String("key", string(key)), errs.String("tried", tried.String()))
}
