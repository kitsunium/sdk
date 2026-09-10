// Package i18n — declares the sentinel *errs.Error outcomes this layer owns.
// Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// The port's own verdicts — a malformed tag, a malformed pattern, a missing
// argument, a missing key — live in internal/core/i18n. Only the outcomes a
// CONCRETE catalogue can produce and an abstract one cannot are declared here.
package i18n

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). Every sentinel in this file is a
// permanent wiring or catalogue fault raised while a store is being BUILT: the
// same files will be refused forever, and the fix is an edit to a catalogue or
// to the rule table, never a retry.
const exitConfig int = 78

var (
	// UnsupportedLanguage is returned when a catalogue registers a language
	// this SDK has no reviewed CLDR plural rule for.
	//
	// It is a CONSTRUCTION-time refusal and it is the point of the domain.
	// The tempting alternative is to fall back to English's two categories,
	// which "works" — the page renders, the tests pass, nothing is logged —
	// and puts a number-agreement error into every Polish, Russian and Arabic
	// sentence the application will ever print. Nobody on an English-speaking
	// team can see that reading the diff, and the users who can see it have
	// no way to report it that reaches the right file.
	//
	// So the language is named, the supported set is named beside it, and the
	// program does not start. Adding a language is a rule plus its CLDR
	// citation plus its table test, in one change — see
	// internal/service/i18n/CLAUDE.md §Adding a language.
	UnsupportedLanguage = errs.Define(CodeUnsupportedLanguage, "UNSUPPORTED_LANGUAGE",
		"No reviewed plural rule exists for this language",
		"service/i18n: the language has no entry in the hand-written CLDR rule table; the fields carry the tag and the supported set — a silent fall back to English rules would render a wrong sentence in every language with few or many",
		errs.WithExitCode(exitConfig))

	// CatalogInvalid is returned by NewStore for a catalogue it cannot
	// compile. The fields name the tag, the key and what was wrong.
	CatalogInvalid = errs.Define(CodeCatalogInvalid, "CATALOG_INVALID",
		"The message catalogue is not usable and was refused",
		"service/i18n: NewStore received a catalogue with a duplicate tag, an entry of the wrong shape, an unknown CLDR category, or a fallback language it does not itself hold",
		errs.WithExitCode(exitConfig))

	// CatalogLoadFailed is returned by LoadFS when the catalogue directory
	// cannot be read or its bytes cannot be decoded.
	//
	// The cause is wrapped rather than replaced, so errors.Is against
	// fs.ErrNotExist and fs.ErrPermission keeps answering — the same
	// discipline internal/service/vfs applies to its read failures.
	CatalogLoadFailed = errs.Define(CodeCatalogLoadFailed, "CATALOG_LOAD_FAILED",
		"The message catalogue could not be read or decoded",
		"service/i18n: LoadFS could not list the directory, read a file, resolve the codec format, or decode a catalogue file; the fields carry the path and the cause",
		errs.WithExitCode(exitConfig))

	// TranslationIncomplete is returned by NewStore for a plural message that
	// does not carry every CLDR category its language can produce.
	//
	// This is the check a translator cannot perform by reading their own
	// file: a Polish entry with `one` and `other` looks finished, and is
	// incomplete only against a rule table stored elsewhere. Deferring it to
	// render time would mean discovering it on the request that first needed
	// `few` — in production, in a language the on-call engineer does not
	// read — and the only repair available there is to show something wrong.
	TranslationIncomplete = errs.Define(CodeTranslationIncomplete, "TRANSLATION_INCOMPLETE",
		"A plural message is missing a form its language requires",
		"service/i18n: the message does not carry every CLDR category the registered language's rules can produce; the fields carry the tag, the key and the missing form",
		errs.WithExitCode(exitConfig))

	// NegotiationEmpty is returned when a negotiation is set up over no
	// supported tags at all.
	//
	// ADR 0031's refuse half: an empty set is not "accept anything" and it is
	// not "always the default" — it is an unfinished wiring, and both
	// readings would hide it behind a working-looking program.
	NegotiationEmpty = errs.Define(CodeNegotiationEmpty, "NEGOTIATION_EMPTY",
		"Language negotiation was set up with no supported languages",
		"service/i18n: the supported set was empty, so every negotiation could only return the default — a wiring fault, refused rather than honoured",
		errs.WithExitCode(exitConfig))
)

// failLoad wraps a filesystem or codec cause onto [CatalogLoadFailed], keeping
// the cause IN THE CHAIN rather than flattening it into a field.
//
// That distinction is the whole reason this helper exists instead of a
// String("cause", err.Error()): a caller needs to tell "there is no catalogue
// directory" from "the catalogue will not compile", and the only thing that
// answers that is errors.Is(err, fs.ErrNotExist). Rendering the cause into a
// field loses it — the text survives and the sentinel does not. Same shape and
// same reason as internal/service/vfs.failRead.
func failLoad(cause error, fields ...errs.FieldValue) error {
	//: the cause stays in the chain — that is the contract.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     CodeCatalogLoadFailed,
		Reason:   "CATALOG_LOAD_FAILED",
		Public:   "The message catalogue could not be read or decoded",
		Private:  "service/i18n: a listing, a file read or a codec decode failed; the cause is wrapped, not replaced, so errors.Is against fs.ErrNotExist still answers",
		ExitCode: exitConfig,
	}, fields...)
}
