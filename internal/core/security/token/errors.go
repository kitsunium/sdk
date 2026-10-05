// Package token — ranges 0.2.13.* (the domain's verdicts) and 0.3.44.* (what
// is specific to the two concrete formats) — ADR 0042, declared here since
// ADR 0160.
//
// Package token — declares the sentinel *errs.Error verdicts an issuer or a
// verifier returns, and the refusals specific to the two concrete formats the
// engine in internal/service/security/token implements (ADR 0160). Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// The Public strings are the half a third party reads (ADR 0005 §4). They name
// WHICH check refused the token and never echo a value from it: not the
// audience, not the issuer, not the key id, not a claim. A rejection message
// that quotes the token's own contents back is a mirror an attacker can query.
package token

// exitDataErr matches sysexits EX_DATAERR (65): the input was not readable as
// a token at all, independently of any key or policy.
const exitDataErr int = 65

// exitNoPerm matches sysexits EX_NOPERM (77): the token was read, and refused.
// The distinction from EX_DATAERR is the one an operator wants at 3am —
// "malformed" is a client bug, "refused" is an authentication decision.
const exitNoPerm int = 77

// exitConfig matches sysexits EX_CONFIG (78): the issuer or verifier itself is
// wrong, so every call fails until the construction site changes (ADR 0031).
const exitConfig int = 78

// statusUnauthorized is HTTP 401. Every refusal in this package maps to it:
// RFC 6750 §3.1 spends "invalid_token" on precisely this set, and a 400 would
// tell a client to change its request when the answer is to obtain a new token.
const statusUnauthorized int = 401
