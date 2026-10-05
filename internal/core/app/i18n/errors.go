// Package i18n — declares the sentinel *errs.Error outcomes of the domain: the
// port's own verdicts — a malformed tag, a malformed pattern, a missing
// argument, a missing key — and the outcomes only a concrete catalogue can
// produce. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
package i18n

// exitConfig matches sysexits EX_CONFIG (78). Every sentinel carrying it is a
// permanent wiring fault raised while a catalogue or a value is being BUILT:
// the same input will be refused forever, and the fix is a catalogue or code
// change, never a retry.
const exitConfig int = 78
