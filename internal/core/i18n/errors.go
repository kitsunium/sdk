// Package i18n — declares the sentinel *errs.Error port outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
package i18n

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). Every sentinel carrying it is a
// permanent wiring fault raised while a catalogue or a value is being BUILT:
// the same input will be refused forever, and the fix is a catalogue or code
// change, never a retry.
const exitConfig int = 78

// The Publics below name the KEY, the PLACEHOLDER, the FORM and the TAG — the
// developer's own identifiers, which appear in the catalogue file the
// developer wrote — and never an [Args] VALUE.
//
// That asymmetry is the security property, and it is the same one
// internal/core/validation, internal/core/authz and internal/service/view each
// state for themselves. A value is the untrusted half: a username, a filename,
// an account number, a token someone pasted into a form. An identifier is the
// trusted half and is useless to an attacker who can already read the binary.
//
// TestNoErrorEverNamesAnArgumentValue is the executable guard: it drives every
// error path in this package with a recognisable secret as an argument value
// and asserts the secret appears in neither Error(), Public(), Private(), nor
// any field.
var (
	// InvalidTag is returned by [ParseTag] for a tag outside the subset this
	// domain resolves.
	//
	// It is deliberately NOT what an unparsable Accept-Language element
	// produces. That header is written by a stranger, and RFC 4647 §3.4
	// prescribes exactly one response to a malformed range — ignore it — so
	// internal/service/i18n's negotiation skips the element and never
	// surfaces an error. Failing a request because a browser sent a peculiar
	// header is the mistake ADR 0051 already refused for `traceparent`.
	InvalidTag = errs.Define(CodeInvalidTag, "INVALID_TAG",
		"The language tag is not one this SDK resolves",
		"core/i18n: ParseTag refused a tag outside the language[-Script][-REGION] subset; the fields carry the tag and the reason",
		errs.WithExitCode(exitConfig))

	// InvalidKey is returned when a message key is empty or carries a control
	// character.
	InvalidKey = errs.Define(CodeInvalidKey, "INVALID_KEY",
		"The message key is empty or contains a control character",
		"core/i18n: a message key must be non-empty and free of control characters — it is compared by byte equality and is shown on screen when a translation is missing",
		errs.WithExitCode(exitConfig))

	// InvalidPattern is returned by [NewMessage] and [NewPluralMessage] for a
	// pattern the placeholder parser refuses.
	InvalidPattern = errs.Define(CodeInvalidPattern, "INVALID_PATTERN",
		"The message pattern is malformed and was refused",
		"core/i18n: the pattern carries an unclosed brace, an unmatched closing brace, an empty placeholder or a placeholder name outside [A-Za-z_][A-Za-z0-9_]*",
		errs.WithExitCode(exitConfig))

	// ArgumentMissing is returned by [MessageValue.Format] when [Args] does not
	// carry a placeholder the pattern names.
	//
	// It is an error rather than an empty substitution because an empty
	// substitution renders "Welcome, " and ships. The name of the absent
	// placeholder travels as a field; no other argument is named, and no
	// value is.
	ArgumentMissing = errs.Define(CodeArgumentMissing, "ARGUMENT_MISSING",
		"A placeholder the message names was not supplied",
		"core/i18n: Format found a placeholder with no matching entry in Args; the fields carry the placeholder name and never any argument value")

	// MessageNotFound is returned when no language in the resolution chain
	// holds the key.
	//
	// The renderer returns it ALONGSIDE a non-empty string — the key. A
	// caller that checks the error fails the render; a caller that ignores it
	// ships "checkout.button.pay" to the screen, which is visible, greppable
	// and obviously wrong. The alternative, an empty string, is a blank space
	// nobody files a bug about.
	MessageNotFound = errs.Define(CodeMessageNotFound, "MESSAGE_NOT_FOUND",
		"No translation exists for this message key",
		"core/i18n: the key was absent from the requested language, from its parents and from the fallback; the fields carry the key and the tags tried")

	// PluralFormMissing is returned by [MessageValue.Format] for a CLDR category
	// the message does not carry.
	PluralFormMissing = errs.Define(CodePluralFormMissing, "PLURAL_FORM_MISSING",
		"The message does not carry the plural form this quantity requires",
		"core/i18n: Format was asked for a CLDR category the message has no pattern for; a catalogue built by internal/service/i18n cannot produce this — completeness is checked at load")

	// InvalidCount is returned by [Decimal] for a quantity whose CLDR
	// operands cannot be derived.
	InvalidCount = errs.Define(CodeInvalidCount, "INVALID_COUNT",
		"The quantity cannot be described by CLDR plural operands",
		"core/i18n: Decimal received a NaN, an infinity, or a fraction-digit count outside 0..maxFractionDigits",
		errs.WithExitCode(exitConfig))

	// InvalidForm is returned by [ParseForm] for a category name outside the
	// six CLDR defines.
	InvalidForm = errs.Define(CodeInvalidForm, "INVALID_FORM",
		"The plural category name is not one of the six CLDR defines",
		"core/i18n: ParseForm accepts exactly zero, one, two, few, many and other — an unknown name is refused rather than ignored, since a typo of other leaves no fallback form",
		errs.WithExitCode(exitConfig))
)
