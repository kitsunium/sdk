// Package token — ranges 0.2.13.* (the domain's verdicts) and 0.3.44.* (what
// is specific to the two concrete formats) — ADR 0042, declared here since
// ADR 0160.
package token

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.13.0 - 0.2.13.255

// CodeMalformed identifies a token whose structure could not be read at all:
// the wrong number of segments, a segment that is not unpadded base64url, a
// header or payload that is not a JSON object.
const CodeMalformed errs.Code = 0x00_02_0D_01 // 0.2.13.1

// CodeAlgorithmNone identifies a token presenting the unsecured "none"
// algorithm of RFC 7519 §6 (in any capitalisation). It is always refused and
// there is no configuration that enables it.
const CodeAlgorithmNone errs.Code = 0x00_02_0D_02 // 0.2.13.2

// CodeAlgorithmMismatch identifies a token whose algorithm header names an
// algorithm other than the one the verifier is bound to. This is the code an
// algorithm-confusion attempt produces.
const CodeAlgorithmMismatch errs.Code = 0x00_02_0D_03 // 0.2.13.3

// CodeSignatureInvalid identifies a token whose signature or MAC tag did not
// authenticate under the bound key.
const CodeSignatureInvalid errs.Code = 0x00_02_0D_04 // 0.2.13.4

// CodeExpired identifies an authenticated token whose "exp" claim is in the
// past, after the configured leeway.
const CodeExpired errs.Code = 0x00_02_0D_05 // 0.2.13.5

// CodeNotYetValid identifies an authenticated token whose "nbf" claim is in
// the future, after the configured leeway.
const CodeNotYetValid errs.Code = 0x00_02_0D_06 // 0.2.13.6

// CodeExpiryRequired identifies a token carrying no "exp" claim where the
// profile requires one — the default. It is also raised by an issuer asked to
// mint a token with neither an explicit expiry nor a configured lifetime.
const CodeExpiryRequired errs.Code = 0x00_02_0D_07 // 0.2.13.7

// CodeAudienceMismatch identifies an authenticated token whose "aud" claim
// does not contain the audience the verifier was configured with.
const CodeAudienceMismatch errs.Code = 0x00_02_0D_08 // 0.2.13.8

// CodeIssuerMismatch identifies an authenticated token whose "iss" claim is
// not the issuer the verifier was configured with.
const CodeIssuerMismatch errs.Code = 0x00_02_0D_09 // 0.2.13.9

// CodeTooLarge identifies an input past a declared size bound: the token
// string, the decoded header, or the number of claims.
const CodeTooLarge errs.Code = 0x00_02_0D_0A // 0.2.13.10

// CodeTooDeep identifies a claims payload whose JSON nesting exceeds the
// configured depth bound.
const CodeTooDeep errs.Code = 0x00_02_0D_0B // 0.2.13.11

// CodeKeyUnsuitable identifies a key that cannot be used for the algorithm it
// was handed to: the wrong length, the wrong curve, or a key type for which
// this domain implements no algorithm.
const CodeKeyUnsuitable errs.Code = 0x00_02_0D_0C // 0.2.13.12

// CodePolicyMisconfigured identifies an issuer or verifier built with a
// configuration it cannot honour — a negative or absurd leeway, a negative
// lifetime, a bound below the floor. Every call is refused without touching
// the token (ADR 0031).
const CodePolicyMisconfigured errs.Code = 0x00_02_0D_0D // 0.2.13.13

// CodeIssueFailed identifies an issuer that could not render the claims it was
// given — a claim the target format cannot express, or a signing fault.
const CodeIssueFailed errs.Code = 0x00_02_0D_0E // 0.2.13.14

// CodeClaimNameInvalid identifies a private claim whose name is empty or
// shadows one of the seven registered claim names.
const CodeClaimNameInvalid errs.Code = 0x00_02_0D_0F // 0.2.13.15

// CodeLifetimeTooLong identifies an authenticated token whose exp-iat span
// exceeds the maximum lifetime the verifier accepts.
const CodeLifetimeTooLong errs.Code = 0x00_02_0D_10 // 0.2.13.16

// range: 0.3.44.0 - 0.3.44.255
//
// What is specific to the two concrete formats the engine in
// internal/service/security/token implements — header parameters, key-set
// selection, PASETO framing. The verdicts a caller matches on are the 0.2.13.*
// block above. The range was allocated in the service layer (LL = 3) and is
// declared here since ADR 0160: LL records the layer that allocated a range,
// not the directory its declaration lives in, so the values never change.

// CodeHeaderUnsupported identifies a JOSE header the engine will not act on:
// a non-empty "crit" (RFC 7515 §4.1.11 makes an unrecognised critical
// parameter a mandatory rejection), or a "typ" that does not match the one the
// verifier requires.
const CodeHeaderUnsupported errs.Code = 0x00_03_2C_01 // 0.3.44.1

// CodeKeyNotFound identifies a token whose "kid" names no key in the JWK Set
// the verifier was built from.
const CodeKeyNotFound errs.Code = 0x00_03_2C_02 // 0.3.44.2

// CodeKeyIDMissing identifies a token presented to a key-set verifier with no
// "kid" header. A set verifier selects by id; it does not try every key it
// holds.
const CodeKeyIDMissing errs.Code = 0x00_03_2C_03 // 0.3.44.3

// CodeKeyIDAmbiguous identifies a "kid" carried by more candidates than the
// verifier will try. Trying a bounded number of candidates is the rotation
// path; trying an unbounded number is a CPU amplifier a publisher controls.
const CodeKeyIDAmbiguous errs.Code = 0x00_03_2C_04 // 0.3.44.4

// CodeFooterMismatch identifies a PASETO token whose footer is not the one the
// verifier expects — including a footer present where none was configured.
const CodeFooterMismatch errs.Code = 0x00_03_2C_05 // 0.3.44.5

// CodeSchemeUnsupported identifies a PASETO version+purpose the engine does
// not implement: any local (encrypted) purpose, and every version other than
// v4. See internal/service/security/token/CLAUDE.md for why v4.local is
// absent.
const CodeSchemeUnsupported errs.Code = 0x00_03_2C_06 // 0.3.44.6

// CodeDuplicateMember identifies a JOSE header or claims object carrying the
// same member name twice. Go's encoding/json silently keeps the last one, so
// two readers of the same bytes could disagree about what the token says
// (RFC 8725 §2.6).
const CodeDuplicateMember errs.Code = 0x00_03_2C_07 // 0.3.44.7
