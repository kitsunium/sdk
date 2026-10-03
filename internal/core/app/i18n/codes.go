// Package i18n — ranges 0.2.30.* (ADR 0063 core/app/i18n block) and 0.3.60.*
// (ADR 0063 service/app/i18n block, declared here since ADR 0160).
package i18n

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.30.0 - 0.2.30.255

// CodeInvalidTag identifies a language tag this domain will not resolve: the
// empty string, a subtag of an illegal length, a non-alphanumeric character,
// or a shape outside the `language[-Script][-REGION]` subset the domain
// implements. The BCP 47 constructs outside that subset — extensions,
// private-use, variants, grandfathered tags — are refused here BY NAME rather
// than parsed and dropped, because a dropped subtag changes which language
// answers without changing anything a reader can see.
const CodeInvalidTag errs.Code = 0x00_02_1E_01 // 0.2.30.1

// CodeInvalidKey identifies a message key that is empty or carries a control
// character. A key is a program identifier, not prose: it is compared by byte
// equality, it appears in error fields, and — when a translation is missing —
// it is what the renderer shows on screen.
const CodeInvalidKey errs.Code = 0x00_02_1E_02 // 0.2.30.2

// CodeInvalidPattern identifies a message pattern the parser refuses: an
// unclosed "{", a "}" with no opening brace, an empty placeholder name, or a
// name outside [A-Za-z_][A-Za-z0-9_]*.
//
// It is a CONSTRUCTION-time refusal on purpose. A pattern is parsed once, when
// the catalogue is built, so a malformed translation fails at startup rather
// than on the one request that happens to reach that key.
const CodeInvalidPattern errs.Code = 0x00_02_1E_03 // 0.2.30.3

// CodeArgumentMissing identifies a render whose [Args] does not carry a
// placeholder the pattern names. The error names the KEY and the PLACEHOLDER
// and never a value — there is no value, and the neighbouring arguments that
// were supplied are not named either.
const CodeArgumentMissing errs.Code = 0x00_02_1E_04 // 0.2.30.4

// CodeMessageNotFound identifies a key no language in the resolution chain
// holds. It is an error AND the renderer still returns a string — the key
// itself — because a blank is a defect nobody reports and a key on screen is
// one everybody does. See ADR 0063 §D4.
const CodeMessageNotFound errs.Code = 0x00_02_1E_05 // 0.2.30.5

// CodePluralFormMissing identifies a [MessageValue] asked for a CLDR category it
// does not carry. A catalogue built through internal/service/app/i18n cannot
// produce it: completeness against the language's own rules is checked when
// the catalogue is built. It remains reachable for a Message assembled by
// hand, and it is a refusal rather than a silent fall back to `other`, which
// would render a wrong sentence in a language nobody on the team reads.
const CodePluralFormMissing errs.Code = 0x00_02_1E_06 // 0.2.30.6

// CodeInvalidCount identifies a quantity whose CLDR operands cannot be
// derived: a negative or excessive fraction-digit count, or a value that is
// NaN or infinite.
const CodeInvalidCount errs.Code = 0x00_02_1E_07 // 0.2.30.7

// CodeInvalidForm identifies a CLDR category name outside the six the
// specification defines. An unknown name in a catalogue file is refused rather
// than ignored: a typo of "other" that is dropped leaves the message with no
// fallback form at all.
const CodeInvalidForm errs.Code = 0x00_02_1E_08 // 0.2.30.8

// range: 0.3.60.0 - 0.3.60.255
//
// The outcomes a CONCRETE catalogue can produce and an abstract one cannot.
// The range was allocated to internal/service/app/i18n, which raises these
// codes while it builds a store, and it is declared here with the port's own
// verdicts so that every code of the domain is in one place (ADR 0160). A code
// keeps the value its allocation gave it whichever layer declares it, so the
// layer byte still reads 3.

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
