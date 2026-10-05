// Package token — the immutable claim set every concrete token format decodes
// into and every issuer renders from.
//
// Package token — the copy-on-write setters that build a [ClaimsValue].
//
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
//
// Package token declares the security-token domain: the [Issuer] and
// [Verifier] ports, the immutable [ClaimsValue] every concrete format decodes
// into, and the typed verdicts a caller matches on. Concrete formats — JWT/JWS
// compact (RFC 7519) and PASETO v4 — live in internal/service/security/token; this
// package owns only the contract, so a caller can hold a Verifier without
// knowing which wire format produced it.
//
// # There is no registry, and that is the security decision
//
// Every other pluggable SDK domain resolves an implementation through a
// process-wide registry keyed on a name. A token registry would be keyed on
// the "alg" header — a field the ATTACKER writes. Resolving the verifying
// algorithm from that field is algorithm confusion, the class of bug where an
// RSA/EC public key (public by definition) is fed to HMAC-SHA-256 as a shared
// secret and the forgery verifies. So the algorithm is bound at construction,
// by a constructor that only accepts the ONE key type that algorithm can use,
// and the header is only ever compared against that binding — never consulted
// to choose it. See ADR 0042.
//
// [Algorithm] is a uint8 enum rather than a string for the same reason: the
// unsecured "none" algorithm of RFC 7519 §6 has no representation in this
// type, so no call site can request it and no configuration can enable it. A
// token whose header says "none" is refused with [AlgorithmNone].
package token
