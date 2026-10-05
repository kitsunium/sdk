// Package jwk — the JSON Web Key format's codes, range 0.3.42.* (ADR 0005).
//
// The range was allocated to internal/service/crypto/key/jwk, which reads and
// writes the format, and is declared here, at the same path in the core, since
// ADR 0160: a domain's codes live in its core and the service only uses them.
// The values did not change with the move, and the LL byte still records the
// service layer that allocated them.
//
// Package jwk — declares the sentinel *errs.Error values for JWK / JWK Set
// parsing, serialisation and selection, which internal/service/crypto/key/jwk
// returns. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form. The Private strings name that service package, where each condition
// is detected.
//
// None of these Public strings names a member VALUE: a JWK carries key
// material, and an error message is the one place it must never surface. The
// Private strings name the member and the rule that rejected it, never its
// contents.
package jwk
