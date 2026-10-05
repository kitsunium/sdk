// Package token — one JWK Set member, with its binding already derived.
//
// Package token — the three numeric limits a verification policy resolves.
//
// Package token — the claims <-> JSON conversion shared by both formats.
//
// JWT and PASETO agree on the seven registered claim NAMES and disagree on
// their value encodings: JWT writes NumericDate (RFC 7519 §2) and allows "aud"
// to be a string or an array of strings; PASETO writes RFC 3339 timestamps and
// allows exactly one audience. So the traversal is written once and the two
// encodings are a [claimShape] each — rather than two claim decoders that
// would drift apart on everything except the part that actually differs.
//
// Package token — the one-pass, string-aware JSON nesting counter.
//
// Package token — the Ed25519 signing binding.
//
// Package token — the bounded wire primitives every format here goes through:
// segment splitting, strict base64url, JSON depth and duplicate-member checks,
// and PASETO's pre-authentication encoding.
//
// Every function in this file runs on ATTACKER-CONTROLLED bytes before any key
// is touched, so each one is O(len(input)) with a bound checked first and no
// allocation proportional to a count the attacker chooses.
//
// Package token — the ECDSA P-256 signing binding.
//
// Package token — the HMAC-SHA-256 binding.
//
// Package token — the JWT issuing policy.
//
// Package token — the JWT/JOSE claim-value encoding.
//
// Package token — JSON string rendering that cannot fail.
//
// encoding/json's Marshal returns an error no string or []string can ever
// produce, and discarding it at a dozen call sites is how a real failure
// somewhere else eventually gets discarded too. Rendering the two shapes this
// package actually emits by hand removes the error channel instead of ignoring
// it — and removes a reflective encode from every mint.
//
// Package token — verification against a JWK or a JWK Set.
//
// This file holds the ONLY run-time algorithm selection in the package, and the
// selector is the KEY, never the token. A relying party fetched the key set
// from a publisher it trusts; the "kty"/"crv" members are that publisher's
// statement about their own key, and deriving the algorithm from them is
// exactly as trustworthy as the key itself. Deriving it from the token's "alg"
// header would be trusting the attacker's statement about the relying party's
// key — which is algorithm confusion, spelled out.
//
// Package token — JWS Compact Serialization (RFC 7515): the header, and the
// algorithm gate every verifier runs before a key reaches a primitive.
//
// Package token — the JWS compact issuer.
//
// Package token — the parsed, not-yet-authenticated shape of a compact token.
//
// Package token — the single-key JWS compact verifier.
//
// Package token — the JWT constructor set: one per algorithm, each accepting
// only the Go key type its algorithm can use.
//
// That is not documentation. NewHS256Verifier's parameter is a
// core/crypto.Key — a struct with an unexported field — so the classic
// confusion bug, handing it an EC or RSA public key so the attacker can sign
// with the public half, is a call that does not compile.
//
// Package token — the algorithm-bound key contracts and their constructors.
//
// This file is where algorithm confusion is made unwritable. Each binding
// holds ONE Go key type and reports ONE algorithm, and the bind* helpers below
// are the only way to build one. There is no `bind(alg, []byte)`, because a
// function that takes an algorithm name and a bag of bytes is a function whose
// caller can be talked into passing a public key where a shared secret goes.
//
// Package token — PASETO v4.public: the constructors and the scheme gate.
//
// PASETO is the format that answers the question "what if the algorithm were
// not negotiable". Its header is a version and a purpose — "v4.public." —
// baked into the signed bytes through the pre-authentication encoding, so
// there is no "alg" field to substitute and algorithm confusion has nothing to
// grip. This implementation still binds the key type at construction, because
// the property should hold for the same reason on both formats rather than by
// accident on one of them.
//
// Only v4.public ships. See the package CLAUDE.md §PASETO for v4.local.
//
// Package token — the PASETO v4.public issuer.
//
// Package token — the PASETO v4.public issuing policy.
//
// Package token — the PASETO claim-value encoding.
//
// Package token — the PASETO v4.public verifier.
//
// Package token — the PASETO v4.public verification policy.
//
// Package token — the validated, defaults-applied form of a verification
// config, so the checks run once at construction rather than once per token.
//
// Package token implements the two concrete security-token formats behind the
// core/security/token ports: JWT over JWS Compact Serialization (RFC 7519 + RFC 7515)
// and PASETO v4.public. It is stdlib-only and composes the SDK's own crypto
// schemes — HMAC-SHA-256 from service/crypto/mac/hmacsha2, Ed25519 from
// service/crypto/sign/ed25519sig — so it adds no dependency to internal/service.
//
// # Algorithm confusion is prevented by the constructor set, not by a check
//
// There is no NewVerifier(alg, key) here, and there is no verifier that reads
// the token's "alg" header to decide what to do. There is one constructor per
// algorithm, each accepting only the ONE Go type that algorithm can use:
//
//	NewHS256Verifier(secret crypto.Key,     cfg VerifierConfig)
//	NewES256Verifier(pub *ecdsa.PublicKey,  cfg VerifierConfig)
//	NewEdDSAVerifier(pub ed25519.PublicKey, cfg VerifierConfig)
//
// The textbook algorithm-confusion bug — an RSA/EC public key handed to
// HMAC-SHA-256 as a shared secret, so that anybody holding the public key can
// mint tokens — is a call that does not compile here: *ecdsa.PublicKey is not
// a crypto.Key and never converts to one. Verification then compares the
// header against the binding and refuses a mismatch with
// coretoken.AlgorithmMismatch, before any key reaches any primitive.
//
// [NewVerifierFromJWK] and [NewSetVerifier] are the only places an algorithm is
// chosen at run time, and they choose it from the KEY's "kty"/"crv" — material
// the relying party fetched from a trusted publisher — never from the token.
//
// # What is refused, always
//
//   - "alg":"none" in any capitalisation (RFC 7519 §6), with no option to
//     enable it: the core Algorithm enum has no value that spells it.
//   - a non-empty "crit" header (RFC 7515 §4.1.11).
//   - a token past the configured size, or claims past the configured nesting
//     depth, both checked before any decode.
//   - a repeated JSON member in the header or the claims (RFC 8725 §2.6).
//   - a token with no "exp", unless the profile opts out explicitly.
//
// See the package CLAUDE.md for the RFC 8725 coverage table, including the
// sections this package does NOT address.
//
// Package token — hosts compile-time interface assertions
// — keeping them out of the production
// source so the runtime binary carries no diagnostic-only declarations.
//
// Package token — the claim validation both formats share.
//
// Everything here runs AFTER the signature has verified, and that ordering is a
// security property, not an implementation detail: reporting "expired" for a
// token whose signature was never checked tells an attacker what is inside a
// forgery, and hands the application a claim set it may log.
//
// Package token — the JWT verification policy.
package token
