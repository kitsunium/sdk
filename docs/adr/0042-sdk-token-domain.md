# ADR 0042 — Security-token domain (`token`): the algorithm is bound by the constructor, not read from the token

- **Status**: Accepted
- **Date**: 2026-09-09
- **Deciders**: SDK maintainers
- **Related**: ADR 0013 (crypto domain), ADR 0016 / ADR 0026 / ADR 0029 (no-registry core siblings), ADR 0030 (the zero value must not be the dangerous one), ADR 0031 (clamp or refuse, never inert), ADR 0035 (PP-range ownership), ADR 0039 (a published port is extended by a sibling), ADR 0022 / ADR 0034 (the `third-party` quarantine rule)
- **Amends**: `internal/core/CLAUDE.md` §Purpose — a 13th core sibling

## Context

A JWT verifier is a small amount of code sitting on the authentication path of
everything a service does, and it is the code where a mistake stops being a bug
and becomes a way in. The failure modes are well documented and keep recurring:

1. **Algorithm confusion.** A relying party publishes an EC or RSA public key
   and verifies `ES256`/`RS256` with it. An attacker presents a token whose
   header says `HS256`, using that *public* key as the HMAC shared secret. A
   library that reads `alg` from the header to select the verification path
   computes the MAC with the public key, the tag matches, and the forgery
   authenticates as anybody.
2. **`alg: none`.** RFC 7519 §6 defines an unsecured JWS. A library that
   implements it, or that treats an unknown `alg` permissively, accepts a token
   with no signature at all.
3. **Unbounded parsing.** CVE-2025-30204 in `golang-jwt` was a
   `strings.Split(token, ".")` performed before any length check: an input of a
   few megabytes of `.` allocated a slice header per separator, so a request the
   size of a photo became hundreds of megabytes of garbage.
4. **Optional expiry.** RFC 7519 §4.1.4 makes `exp` OPTIONAL. A conforming
   token can therefore be valid forever, and a verifier that only checks `exp`
   *when present* accepts it.

The SDK already ships everything the primitives need — HMAC-SHA-256, Ed25519,
ECDSA P-256, and (since the JWK format landed) RFC 7517 key representation with
`kid` selection. What is missing is the piece that composes them without
reintroducing the four failure modes above.

## Decision

### D1 — `internal/core/token` as the 13th core sibling, with NO registry

The port is two one-method interfaces and one immutable value:

```go
type Issuer   interface { Issue(claims ClaimsValue) (string, error) }
type Verifier interface { Verify(token string) (ClaimsValue, error) }
```

Every other pluggable SDK domain resolves an implementation through a
process-wide registry keyed on a name. **This one must not**, and the reason is
not shape preference: a token registry's key would be the `alg` header, and the
`alg` header is written by the attacker. Resolving the verifying algorithm from
it IS failure mode 1. `proc` (ADR 0016), `resilience` (ADR 0026) and `net`
(ADR 0029) are the no-registry precedents; this is the first sibling where the
absence of a registry is a security property.

`Algorithm` is a `uint8` enum, not a string, for the same reason: **`none` has
no representation in the type.** There is no `Algorithm` value that renders the
string a `none` header would carry, so no call site can request one and no
configuration can enable one. `TestNoAlgorithmSpellsNone` scans the entire
`uint8` range so that stays true.

### D2 — algorithm confusion is made *unwritable*, then also checked

There is no `NewVerifier(algorithm, key)`. There is one constructor per
algorithm, each accepting only the single Go type that algorithm can use:

```go
NewHS256Verifier(secret crypto.Key,      cfg VerifierConfig)
NewES256Verifier(pub *ecdsa.PublicKey,   cfg VerifierConfig)
NewEdDSAVerifier(pub ed25519.PublicKey,  cfg VerifierConfig)
```

`core/crypto.Key` is a struct with an unexported field. An `*ecdsa.PublicKey`
does not convert to one, so **"verify this with the EC public key as an HMAC
secret" is a call that does not compile.** That is the structural half, and it
is the half that matters: a check can be forgotten, a type cannot.

The runtime half closes the rest. `checkHeader` COMPARES the token's `alg`
against the algorithm the verifier was constructed with and returns
`AlgorithmMismatch` on a difference — **before any key material reaches any
primitive**. The header is never used to select a key, an algorithm, or a
branch.

`NewVerifierFromJWK` and `NewSetVerifier` are the only run-time algorithm
selection in the domain, and they select from the **key's** `kty`/`crv`. That is
the publisher's statement about their own key, fetched by the relying party from
somewhere it trusts — exactly as trustworthy as the key itself, and categorically
different from the token's statement about that key.

### D3 — expiry is required by default; the opt-out has a name

RFC 7519 leaves `exp` optional. A bearer token that never expires is a password
with a worse rotation story, so this domain inverts the default: a token with no
`exp` is refused with `ExpiryRequired`, on **both** sides — an issuer will not
mint one either, so a token this SDK produces can never be one this SDK would
refuse. The opt-out is `AllowMissingExpiry`, whose zero value is the safe one
(ADR 0030). A recipient can go further with `MaxLifetime`, which refuses an
authenticated token whose `exp - iat` span is longer than it accepts — the only
thing a recipient has instead of revocation.

### D4 — every bound is checked before the work it funds

`splitCompact` checks `MaxTokenLen` **first**, then walks with
`strings.IndexByte` into a fixed `[4]string` array whose entries are views into
the caller's string. There is no `strings.Split` anywhere in the domain.
`decodeSegment` checks `DecodedLen` before decoding. `checkJSONDepth` is a
linear, string-aware scan run before `encoding/json` builds a frame per level.
`MaxPrivateClaims` caps the member count; `MaxKeyCandidates` caps how many
signature verifications one `kid` can cost. Every bound is configurable within a
documented range and refused outside it (ADR 0031).

### D5 — signature first, claims second, one verdict

`Verify` is: parse → compare the algorithm → verify the signature → decode and
judge the claims. Reporting "expired" for a token whose signature was never
checked tells an attacker what is inside a forgery and hands the application a
claim set out of one. A refusal returns the **zero** `ClaimsValue`; there is no
"invalid, but here are the claims anyway" path.

`ClaimsValue` renders its SHAPE under every `fmt` verb —
`token.Claims{registered:3 aud:1 private:2}` — and never a value. A claim set is
the output of an authentication decision and routinely holds a subject id, an
email, a tenant.

### D6 — PASETO v4.public ships; v4.local does not, and says why

`v4.public` (Ed25519 over the pre-authentication encoding) is implemented,
including the authenticated footer and v4's implicit assertion. It is worth
shipping alongside JWT precisely as the contrast: PASETO bakes the version and
purpose into the signed bytes, so there is no negotiable algorithm field for
confusion to grip.

**`v4.local` is not implemented.** It requires XChaCha20 and BLAKE2b, neither of
which is in the Go standard library. The SDK's only XChaCha lives in
`third-party/x-crypto/xchacha` over `golang.org/x/crypto`, deliberately, so that
`internal/service` — and therefore every `pkg/v1` consumer — stays dep-light
(ADR 0013). Pulling `x/crypto` into `internal/service` for one token purpose is
the same violation ADR 0022/0034 quarantined `hcl` for. So `v4.local` is refused
by NAME (`SchemeUnsupported`, with the scheme in a field) rather than as
"malformed": an operator holding a v4.local token has a missing feature, not a
corrupt token. If it ever ships, it ships under `third-party/`.

### D7 — error blocks `0.2.13.*` and `0.3.44.*`

Core owns the verdicts a caller matches on — `MALFORMED`, `ALGORITHM_NONE`,
`ALGORITHM_MISMATCH`, `SIGNATURE_INVALID`, `EXPIRED`, `NOT_YET_VALID`,
`EXPIRY_REQUIRED`, `AUDIENCE_MISMATCH`, `ISSUER_MISMATCH`, `TOO_LARGE`,
`TOO_DEEP`, `KEY_UNSUITABLE`, `POLICY_MISCONFIGURED`, `ISSUE_FAILED`,
`CLAIM_NAME_INVALID`, `LIFETIME_TOO_LONG`. Service owns what is specific to the
two formats — `HEADER_UNSUPPORTED`, `KEY_NOT_FOUND`, `KEY_ID_MISSING`,
`KEY_ID_AMBIGUOUS`, `FOOTER_MISMATCH`, `SCHEME_UNSUPPORTED`,
`DUPLICATE_MEMBER`. Both ranges are registered in `codeRangeOwners` (ADR 0035).

Every refusal carries **HTTP 401**: RFC 6750 §3.1 spends `invalid_token` on
exactly this set, and a 400 would tell a client to change its request when the
answer is to obtain a new token.

## Consequences

- 13th core sibling; the `internal/core` purpose statement widens. Docs and
  `docs/error-codes.yaml` are updated in the same commit (rule 11).
- `pkg/v1/token` gains a dep-light facade with ten constructors, 23 sentinels
  and 23 re-exported codes.
- The domain composes the crypto domain rather than reimplementing it, with one
  documented exception: **ES256 signing/verification goes to `crypto/ecdsa`
  directly**, because `service/crypto/ecdsasig` speaks ASN.1/DER — the right
  encoding for X.509 and the wrong one for JOSE, which mandates fixed-width
  `R||S` (RFC 7518 §3.4). Transcoding DER to `R||S` would mean re-parsing an
  attacker-supplied ASN.1 structure on the verify path; producing the JOSE
  encoding directly means one primitive, one encoding, and no converter to get
  wrong.
- Errors in this domain **do not carry their stdlib cause**, which is the
  opposite of the SDK's usual wrapping habit. A stdlib parse error quotes what
  it choked on, and here those bytes came off an unauthenticated token; keeping
  the cause reachable would put token content one `errors.Unwrap` from any log
  line that walks a chain. The secondary effect is that origin-wins preserves
  the sentinel's HTTP status and exit code, which the stdlib-cause path cannot.
  The exception is a `crypto/rand` fault during signing — that is about this
  host, not about anything an attacker sent.

### Cross-platform (ADR 0018)

100 % portable Go (`crypto/*`, `encoding/*`, `strings`, `time`). No OS-specific
code; trivial build bar on all 8 GOOS.

## Security perimeter — RFC 8725 coverage

| Section | Status |
|---|---|
| §3.1 Perform Algorithm Verification | **covered** — the algorithm is bound at construction; the header is only compared |
| §3.2 Use Appropriate Algorithms | **covered** — closed set; `none` unrepresentable |
| §3.3 Validate All Cryptographic Operations | **covered** — one verdict, whole-token, zero claims on failure |
| §3.4 Validate Cryptographic Inputs | **covered** — EC points validated via `ECDH()`, key lengths checked, ES256 signature length exact |
| §3.5 Sufficient Key Entropy | **partial** — length is structural (a 256-bit `crypto.Key`); entropy is not measurable here |
| §3.6 Avoid Compression of Encryption Inputs | **not applicable** — no JWE |
| §3.7 Use UTF-8 | **covered on both sides** — `Issue` refuses invalid UTF-8 in every claim text it writes, and `Verify` refuses a header or claims object that is not UTF-8 before decoding it; base64url decoding is strict. (Amended 2026-09-11: this row said "covered by delegation", but `encoding/json` does not refuse invalid UTF-8 — it replaces each bad byte with U+FFFD, so the SDK minted such tokens and verified two different subjects as one. The check costs 20–30 ns on a realistic claims object.) |
| §3.8 Validate Issuer and Subject | **partial** — `iss` is checked when configured; "the key belongs to that issuer" stays the caller's; `sub` is not validated |
| §3.9 Use and Validate Audience | **covered** — `aud` must contain the configured audience |
| §3.10 Do Not Trust Received Claims | **partial** — `kid` only ever indexes a caller-supplied key set; `jku`/`x5u` are ignored because nothing here fetches; claim-VALUE sanitisation is the application's |
| §3.11 Use Explicit Typing | **covered** — `IssuerConfig.Type` / `VerifierConfig.RequireType` |
| §3.12 Mutually Exclusive Validation Rules | **covered as a mechanism** — `RequireType` + `Issuer` + `Audience` |

Also addressed: **§2.6 (multiplicity of JSON encodings)** — a repeated member
name in the header or the claims is refused, because `encoding/json` keeps the
last occurrence and a reader that keeps the first would disagree about what the
token says; and **strict, unpadded base64url only**, so one token has exactly
one spelling and a replay table keyed on the string cannot be bypassed by
re-encoding it.

## What this domain does NOT guarantee

Stated plainly, because a security perimeter that is only described by what it
covers is not a perimeter:

- **No confidentiality.** There is no JWE and no PASETO `local` purpose. A
  signed token is readable by anybody holding it. A secret in a claim is a
  secret in a base64 string. (§2.3, §2.4 are consequently out of scope.)
- **No replay detection.** `jti` is decoded and returned; nothing remembers it.
  A replay cache is state with a lifetime and an eviction policy, owned by
  whoever owns the store.
- **No revocation.** This domain answers "is this token authentic and
  in-window", never "is it still wanted". `MaxLifetime` is the mitigation it can
  offer.
- **No key retrieval.** Nothing fetches a JWKS, honours `Cache-Control`, or
  follows an OpenID discovery document. That is a connector with its own failure
  modes (SSRF on a caller-supplied URL, staleness, retries) — the same line
  `service/crypto/jwk` draws.
- **No key entropy guarantee** — see §3.5 above.
- **No claim-value sanitisation.** An authenticated `sub` is authenticated, not
  safe to interpolate into a query. Only the application knows its own grammar.
- **No side-channel guarantee beyond the secret comparison.** The HMAC tag
  comparison is `hmac.Equal` and the PASETO footer comparison is
  `subtle.ConstantTimeCompare`. Everything else — parse, decode, claim checks —
  is ordinary code, and its timing varies with input shape.

## Alternatives considered

- **A registry keyed on `alg`, like every other pluggable domain.** Rejected:
  the key would be attacker-controlled. This is D1, and it is the ADR's subject.
- **One `NewVerifier(alg Algorithm, key []byte)` constructor.** Rejected: it
  makes the confusion bug expressible again. A `[]byte` key parameter accepts a
  public key and an HMAC secret with equal enthusiasm; the typed constructors do
  not.
- **`Algorithm` as a string type.** Rejected: `Algorithm("none")` would be
  writable, and the refusal would then be a runtime check rather than an absent
  vocabulary.
- **Supporting RSA (`RS256`/`PS256`).** Rejected for now: the SDK registers no
  RSA signer (ADR 0014 §Out of scope), and adding one for JWT alone would put a
  primitive in the crypto domain that nothing else uses. The consequence is
  stated where it bites — an RSA JWK is refused at PARSE time with
  `UNSUPPORTED_KEY_TYPE` (0.3.42.3), because `service/crypto/jwk` does not model
  RSA at all, so it never reaches a verifier. *(Corrected 2026-09-11: this line
  said the refusal was `KeyUnsuitable`, which is what P-384 and P-521 get at bind
  time; RSA is stopped one step earlier.)* It bites harder than a single key: a
  JWK Set is parsed whole or refused whole, so a published set carrying ONE RSA
  member — the common shape for large identity providers — is refused entirely,
  and loading its usable members means splitting the document and calling
  `ParseJWK` per member.
- **P-384 / P-521 (`ES384`/`ES512`).** Rejected: `service/crypto/jwk` can
  represent them because a published JWK Set routinely carries them, but the SDK
  signs with neither, and a verifier that pretended otherwise would promise
  interop the crypto domain cannot honour.
- **Pulling `x/crypto` into `internal/service` for `v4.local`.** Rejected — D6.
- **Returning claims alongside a validation error**, so a caller can log the
  subject of an expired token. Rejected: the same code path would then be one
  edit away from returning claims out of a token that failed *authentication*,
  which is the difference between a diagnostic and a forgery oracle.

## Breaking changes

None. `token` is a new domain in this change set — there is no prior published
surface to break.

## Deferred

- **RSA (`RS256`/`PS256`) and `ES384`/`ES512`** — see Alternatives. Each needs a
  crypto-domain decision first, not a token-domain one.
- **JWE.** Encryption changes what the domain guarantees, and RFC 8725 §2.3
  (incorrect composition of encryption and signature) is a whole design of its
  own.
- **PASETO `v4.local`** — see D6. Under `third-party/` if ever.
- **Remote JWKS retrieval, with caching and rotation.** Deliberately a
  connector, not a format concern. When it lands it will need its own decisions
  about SSRF, TTL and stale-while-revalidate.
- **A replay store.** Needs an owner for the state.
- **Nested JWTs (`cty: JWT`).** RFC 8725 §3.3 covers them; nothing here needs
  them yet, and each nesting level is a second place to get the algorithm gate
  right.

## References

- Impl: `internal/core/token/`, `internal/service/token/`, `pkg/v1/token/`.
- [RFC 7519](https://www.rfc-editor.org/rfc/rfc7519.html) (JWT),
  [RFC 7515](https://www.rfc-editor.org/rfc/rfc7515.html) (JWS),
  [RFC 7518](https://www.rfc-editor.org/rfc/rfc7518.html) (JWA),
  [RFC 8037](https://www.rfc-editor.org/rfc/rfc8037.html) (Ed25519 for JOSE),
  [RFC 8725](https://www.rfc-editor.org/rfc/rfc8725.html) (JWT BCP),
  [RFC 6750](https://www.rfc-editor.org/rfc/rfc6750.html) §3.1 (bearer-token
  error responses).
- [PASETO specification](https://github.com/paseto-standard/paseto-spec).
- [CVE-2025-30204](https://nvd.nist.gov/vuln/detail/CVE-2025-30204) — the
  unbounded-split class D4 exists to prevent.
- ADR 0013 (crypto domain), ADR 0022 / ADR 0034 (the `third-party` quarantine
  rule D6 follows), ADR 0035 (range ownership), ADR 0039 (frozen ports).
