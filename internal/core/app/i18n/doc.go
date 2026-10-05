// Package i18n — ranges 0.2.30.* (ADR 0063 core/app/i18n block) and 0.3.60.*
// (ADR 0063 service/app/i18n block, declared here since ADR 0160).
//
// Package i18n — the CLDR plural operands of a quantity.
//
// Package i18n — declares the sentinel *errs.Error outcomes of the domain: the
// port's own verdicts — a malformed tag, a malformed pattern, a missing
// argument, a missing key — and the outcomes only a concrete catalogue can
// produce. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
//
// Package i18n — the six CLDR plural categories.
//
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
// renderer live in internal/service/app/i18n, because a table of thirteen
// languages is a fact about the world rather than a contract — and so do the
// parser of a written tag and the compiler of a pattern's text, because
// reading a format is a mechanism (ADR 0160). What they produce is assembled
// here, by [NewTag], [NewPattern] and [NewCompiledMessage], which hold every
// value to its own invariants whoever builds it.
//
// Package i18n — the message-source port and its two ADR 0039 siblings.
//
// Package i18n — the message key, the compiled message, and its rendering.
//
// Package i18n — a compiled message body, and the placeholder-name grammar it
// holds its spans to.
//
// Package i18n — the language tag, and the BCP 47 subset it admits.
package i18n
