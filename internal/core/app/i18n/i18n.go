package i18n

// PluralRule reports which CLDR category a quantity falls in for ONE language.
//
// It is a FUNC port rather than an interface, which satisfies ADR 0039
// structurally: a func type cannot grow a method at all, so publishing it
// through pkg/v1/app/i18n cannot break a downstream implementation later. It is
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
// page goes through internal/service/app/view (ADR 0058), whose contextual
// escaping is aware of where the cursor is; escaping here as well would
// double-escape every apostrophe in every French sentence in the catalogue.
type Args map[string]string
