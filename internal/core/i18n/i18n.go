// Package i18n declares the SDK's message-translation port: the [Catalog] a
// translated message is read from, the [TagValue] that names a language, the
// [MessageValue] a translator wrote, the CLDR plural [Form] a quantity falls
// in and the [CountValue] that decides which. A core sibling admitted by
// ADR 0063.
//
// # What this domain is, and the four things it is not
//
// It renders a message a developer wrote, in the language a request asked for,
// with the plural form the quantity requires. That is the whole subject.
//
// It does NOT format numbers, dates, times, currencies or units; it does not
// collate, case-map, normalise or transliterate. Those are the rest of CLDR
// and they are refused BY NAME rather than half-shipped — see ADR 0063
// §"Refused by name". [Args] is therefore map[string]string: a caller
// substitutes a number by formatting it, and the SDK never pretends the
// formatting was locale-aware when it was not. An Args of `any` would apply Go
// verbs to a French quantity and print "1234.5", which is a bug that looks
// like a feature until it reaches Europe.
//
// # The plural rules follow the MESSAGE, not the request
//
// This is the decision the domain exists to get right. When a request asks for
// Polish and the key was never translated into Polish, the renderer serves the
// message it does have — say the English one. The plural form must then be
// selected with ENGLISH rules, because the English message carries `one` and
// `other` and nothing else. Selecting with Polish rules would ask that message
// for `few`, which it does not have, and the caller would get either a crash,
// a blank, or — worst — the `other` form silently standing in for `few`, which
// is a grammatically wrong sentence in a language nobody on the team reads.
//
// So the rules are a property of the language that ANSWERED. See ADR 0063 §D3.
//
// # A message is complete for its language, or it is refused at load time
//
// A catalogue entry for a language whose rules can produce `few` and does not
// carry a `few` pattern is refused when the catalogue is BUILT, naming the key
// and the form. It is not repaired at render time by falling back to `other`,
// because that repair produces a wrong sentence that nothing observes: the
// page renders, the tests pass, and a Polish reader sees a number agreement
// error on every page. Startup is the only place the fault is cheap.
//
// # Where the pieces live
//
// This package owns the values, the ports and the typed refusals. The plural
// rule TABLE, the concrete catalogue, the Accept-Language negotiation and the
// renderer live in internal/service/i18n, because a table of thirteen
// languages is a fact about the world rather than a contract.
package i18n

// PluralRule reports which CLDR category a quantity falls in for ONE language.
//
// It is a FUNC port rather than an interface, which satisfies ADR 0039
// structurally: a func type cannot grow a method at all, so publishing it
// through pkg/v1/i18n cannot break a downstream implementation later. It is
// the shape internal/core already uses for resilience.Operation,
// scheduler.Job, lifecycle.Start, validation.Constraint and authz.Policy.
//
// A rule MUST be total: every [CountValue] maps to a category, and the
// category is [FormOther] whenever no other clause matches. CLDR guarantees
// `other` exists in every language, which is why [FormOther] is the zero
// [Form] — ADR 0031's "the zero value is the safe one" applied to a port with
// no constructor to refuse in.
type PluralRule func(count CountValue) Form

// Args binds placeholder names to the text that replaces them.
//
// It is map[string]string and deliberately not map[string]any. The values are
// substituted verbatim, so a caller that wants a number formatted for a locale
// must format it — and discovers, at the call site, that this SDK does not
// ship a locale-aware number formatter. An `any` here would call Go's default
// formatting instead and produce "1234.5" for a French reader, which is the
// same bug with the discovery removed.
//
// # Where the values may come from, and where the PATTERNS may not
//
// A value is untrusted: it is a username, a filename, a count. It is inserted
// literally and is never parsed, so a value containing "{other}" produces the
// six characters and no second substitution. That is a property with a test.
//
// A message PATTERN is trusted input — it comes from a catalogue file the
// developer ships, next to the code. A pattern assembled from user input would
// let that user name a placeholder the caller happens to pass and read its
// value; the domain cannot detect that and does not try. See ADR 0063
// §"What is NOT guaranteed".
//
// # Nothing here escapes anything
//
// A rendered message is a string, never a trusted-HTML type. Placing it in a
// page goes through internal/service/view (ADR 0058), whose contextual
// escaping is aware of where the cursor is; escaping here as well would
// double-escape every apostrophe in every French sentence in the catalogue.
type Args map[string]string
