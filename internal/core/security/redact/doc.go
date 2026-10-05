// Package redact — range 0.3.73.* (ADR 0101), declared here since ADR 0160.
//
// Package redact — declares the sentinel *errs.Error outcomes. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Neither refusal carries a byte of what it refused: this domain exists to
// keep secrets out of what is shown, and an error message is shown. They are
// raised by the engine in internal/service/security/redact — their Private
// names that package, the one that raises them — and declared here since ADR
// 0160, so every code of the domain is in one place.
//
// Package redact declares the SDK's display-redaction port: the [Redactor]
// that renders a Go value, a JSON document, a text or log attributes for
// DISPLAY with every secret it recognises replaced, within a byte bound, never
// touching what it is given — and the values every implementation shares: the
// [Placeholder] a secret becomes, the [Ellipsis] a cut ends in, the [MinBytes]
// floor under every bound, the [Unencodable] marker, and the [DocumentValue] a
// JSON rendering returns. A core sibling since ADR 0160, for the domain ADR
// 0101 admitted.
//
// It is for showing data to a person: a developer console, a trace viewer, a
// log panel, an error page. It is not an access control and not a sanitiser
// for data that goes back into a system: a redacted document is a different
// document, and a secret spelled in a way no rule recognises is shown.
//
// The engine — the three rules (a NAME carrying a configured word, a field
// DECLARED secret, a URL's CREDENTIALS), the writer that counts every byte
// before writing it, and the per-type plan cache — lives in
// internal/service/security/redact, which implements [Redactor]. Its
// construction parameters are the engine's (ADR 0074). This package owns the
// contract, the values a caller compares results against, and the domain's
// codes.
package redact
