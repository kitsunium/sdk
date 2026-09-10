// Package i18n — range 0.3.60.* (ADR 0063 service/i18n block).
package i18n

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.60.0 - 0.3.60.255

// CodeUnsupportedLanguage identifies a language this SDK has no reviewed CLDR
// plural rule for.
//
// It is the domain's central refusal. The alternative — falling back to
// English's `one`/`other` rules — produces a grammatically wrong sentence in
// every Slavic and Semitic language, on every page, with no symptom any test
// or monitor can see. See ADR 0063 §D1.
const CodeUnsupportedLanguage errs.Code = 0x00_03_3C_01 // 0.3.60.1

// CodeCatalogInvalid identifies a catalogue that cannot be compiled: a
// duplicate tag, an entry that is neither a pattern nor a map of patterns, an
// unknown CLDR category name, or a fallback language the catalogue does not
// itself hold.
const CodeCatalogInvalid errs.Code = 0x00_03_3C_02 // 0.3.60.2

// CodeCatalogLoadFailed identifies a catalogue directory that could not be
// read or decoded: an absent directory, an unreadable file, an unregistered
// codec format, or bytes the codec refused.
const CodeCatalogLoadFailed errs.Code = 0x00_03_3C_03 // 0.3.60.3

// CodeTranslationIncomplete identifies a plural message that does not carry
// every CLDR category the language it was registered under can produce — a
// Polish entry with only `one` and `other`, when Polish rules also produce
// `few` and `many`.
//
// It has its own code, separate from [CodeCatalogInvalid], because it is the
// one catalogue defect a reviewer cannot see by reading the file: the entry
// looks complete, and it is only incomplete relative to a rule table stored
// somewhere else.
const CodeTranslationIncomplete errs.Code = 0x00_03_3C_04 // 0.3.60.4

// CodeNegotiationEmpty identifies a call to build a renderer with no
// supported tags at all. It is a wiring fault: a negotiation over an empty set
// can only ever return the default, so the caller has built something that
// cannot do its job.
const CodeNegotiationEmpty errs.Code = 0x00_03_3C_05 // 0.3.60.5
