# Notes — <!-- updated: 2026-10-24T00:00:00Z -->

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles. `CLAUDE.md` keeps what to obey; this file holds the rest.

## The decision this package exists to make

**Algorithm confusion is prevented by the constructor set, not by a check.**

There is no `NewVerifier(alg, key)`, and no verifier reads the token's `alg`
header to decide what to do with it. There is one constructor per algorithm,
and each takes only the ONE Go type that algorithm can use:

| Constructor | Key parameter | Bound algorithm |
|---|---|---|
| `NewHS256Verifier` | `core/crypto.Key` | `HS256` |
| `NewES256Verifier` | `*ecdsa.PublicKey` | `ES256` |
| `NewEdDSAVerifier` | `ed25519.PublicKey` | `EdDSA` |
| `NewPasetoV4Verifier` | `ed25519.PublicKey` | `v4.public` |

The textbook attack — take the EC/RSA public key a server publishes, use it as
an HMAC shared secret, mint tokens with it — needs a call that hands a public
key to the HMAC path. **That call does not compile**: `crypto.Key` is a struct
with an unexported field, and no `*ecdsa.PublicKey` converts to one. What is
left is the *runtime* half: `policyValue.checkHeader` COMPARES the header
against the binding and refuses with `AlgorithmMismatch` — before any key
material reaches any primitive.

`NewVerifierFromJWK` and `NewSetVerifier` are the only run-time algorithm
selection in the package, and the selector is the **key**, never the token: a
JWK's `kty`/`crv` is the publisher's statement about their own key, which is
exactly as trustworthy as the key itself.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `token.go` | the shared bounds (`DefaultMaxTokenLen`, `MaxTokenLenCeiling`, `DefaultMaxClaimDepth`, `MaxKeyCandidatesCeiling`, `MaxLeeway`) |
| `issuer_config.go` / `verifier_config.go` | `IssuerConfig` / `VerifierConfig` |
| `paseto_issuer_config.go` / `paseto_verifier_config.go` | the FLAT PASETO configs + their projections onto the shared policy |
| `policy.go` / `bounds_value.go` | `policyValue` — the validated, defaults-applied verification policy |
| `validate.go` | the shared post-authentication claim checks + `stampIssuedClaims` |
| `encoding.go` / `depth_scan.go` | bounded segment split (`splitCompact`, and `splitCompactBytes` for a token held as bytes), strict base64url (`decodeSegment`, and `decodeSegmentInto` a caller's buffer), the JSON depth pre-pass, PASETO `PAE` |
| `json_scan.go` | the reflection-free strict RFC 8259 reader every claims object and every JOSE header goes through — `scanObject` indexes the top-level members into a fixed array and refuses malformed text, a duplicate or escaped member name, a lone surrogate, a number a float64 cannot hold and a member count past the cap; `appendJSONString` / `decodeTextInto` / `jsonStringEqual` decode or compare a validated literal without allocating; see §One refusal set |
| `jsonstring.go` | `quoteJSONString` / `quoteJSONStrings` — JSON string rendering with no error channel |
| `claims_codec.go` | `readClaims` — THE refusal set: UTF-8, depth, the scan, the registered claims (`readRegistered`: a registered claim sent as JSON null is refused — a lenient reader decodes `"aud": null` as an audience of `[""]` and `"nbf": null` as 1970; a private claim may still be null), the private names (`checkPrivateMembers`) — then `decodeClaims` builds the `ClaimsValue` from the index; the `claimShape` contract and its closed dispatch (`decodeTimeIn`, `checkAudienceIn` reach each shape's own `decodeTime` / `checkAudience` by a type switch, so the interface does not carry those two — a call through it would make the hot path's argument escape); the issue-side UTF-8 refusal (`checkClaimText`, `putPrivate`) |
| `jose_shape.go` / `paseto_shape.go` | the two claim-value encodings (NumericDate vs RFC 3339) |
| `jws.go` / `jws_parts.go` | `headerView` / `readJOSEHeader` (the header through the same scan), `headerValue`, `checkHeader` and `checkHeaderView` — two readers, one `judgeHeader` (the algorithm gate) — `jwsPartsValue` |
| `hs256_verifier.go` | `hs256Verifier` — every HS256 verifier, built from a `Key` or an oct JWK — and its `VerifierInto`: `NewScratch`, `VerifyInto` (structure, alg, MAC through the caller's scratch, then `judgeInto`: the one refusal set, the policy, and the copy into the `ClaimsBuf`); header, signature and claims decoded into stack arrays |
| `verify_into_*_test.go` / `scratch_shared_external_test.go` | `VerifyInto` against `Verify`, the check order, the buffer reused across six requests with its whole memory scanned, the foreign and busy scratch (no MAC computed), the redaction under every verb; the race-off zero-allocation gate; and one scratch shared across goroutines with forged tokens carrying copied signatures — none accepted |
| `legacy_reader_test.go` / `legacy_differential_test.go` | the encoding/json reader the scanner replaced, kept as the ORACLE, and the four-case differential test and `FuzzClaimsReader` that judge the scanner against it |
| `jws_issuer.go` / `jws_verifier.go` | the JWS issuer and single-key verifier |
| `jwt.go` | the six JWT constructors |
| `keys.go` + `hs256_binding.go` / `es256.go` / `ed25519.go` | the algorithm-bound key contracts and their five implementations (each curve's signing and verifying halves share one file) |
| `paseto.go` / `paseto_issuer.go` / `paseto_verifier.go` | PASETO v4.public |
| `jwkbridge.go` | `NewVerifierFromJWK`, `NewSetVerifier`, `setVerifier`, `indexByKid` (the set is bound ONCE, at construction — see §Cost) + `selectable` (a set no token could verify against is refused `POLICY_MISCONFIGURED`) |
| `bound_key_value.go` | `boundKeyValue` — one JWK Set member with its verifying binding already derived, and whether that derivation succeeded |
| `token_compliance.go` | the compile-time contract assertions |

## RFC 8725 coverage — what is addressed, and what is not

| Section | Status | How |
|---|---|---|
| §3.1 Perform Algorithm Verification | **covered** | The algorithm is bound at construction and the header is only ever compared to it. There is no way to express "accept whatever the token says". |
| §3.2 Use Appropriate Algorithms | **covered** | Closed set: HS256, ES256, EdDSA, PASETO v4.public. `none` has no representation in the `Algorithm` enum. Agility is a new constructor, not a config string. |
| §3.3 Validate All Cryptographic Operations | **covered** | One verdict, whole-token: a failed signature returns `SignatureInvalid` and the ZERO claim set. There is no partial-acceptance path. |
| §3.4 Validate Cryptographic Inputs | **covered** | EC public keys are validated as curve points via `(*ecdsa.PublicKey).ECDH()`, not merely measured; an ES256 private key's scalar must be present, in range, and derive exactly its declared public point (go1.27's `ecdsa` dereferences a nil `D` on the first `Sign`, and signs with a mismatched one without comparing); Ed25519 key lengths are checked before the stdlib would panic; ES256 signatures must be exactly 64 octets. |
| §3.5 Sufficient Key Entropy | **partial** | The LENGTH half is structural: HS256 takes a `core/crypto.Key`, fixed at 256 bits, so a short passphrase cannot become one. The ENTROPY half is **not measurable here** — a 32-byte key of ASCII is still 32 bytes. Derive one (`pkg/v1/crypto/kdf`); do not type one. |
| §3.6 Avoid Compression of Encryption Inputs | **not applicable** | No JWE, no compression. |
| §3.7 Use UTF-8 | **covered on both paths** | An issuer refuses every string it would write that is not valid UTF-8: `iss`/`sub`/`jti`/each `aud` and every private claim's name and bytes at `Issue` (`ISSUE_FAILED`), and the configured `Issuer`/`Type`/`KeyID` at construction (`POLICY_MISCONFIGURED`). Nothing downstream would: `quoteJSONString` passes bytes through, `json.Marshal` REWRITES a bad map key to U+FFFD, and `encoding/json`'s decoder turns `a\xff` and `a\xfe` into one string — so two subjects the caller kept apart would verify as one. A token some OTHER issuer wrote is held to the same rule on the way in: `parseJOSEHeader` and `decodeClaims` refuse a header or claims object that is not UTF-8 (`MALFORMED`) before `encoding/json` can replace a bad byte — without it, subjects `a\xff` and `a\xfe` both verified as `a\ufffd` (`TestTextThatIsNotUTF8IsRefusedOnVerify`). It costs 20–30 ns on a realistic claims object. The base64url decoder is strict, so there is no second encoding to admit either. |
| §3.8 Validate Issuer and Subject | **partial** | `VerifierConfig.Issuer` is checked when set. The section's stronger requirement — that the KEY belongs to the claimed issuer — is the caller's: this package verifies against the key it was handed and cannot know whose it is. `NewSetVerifier` narrows it (the set comes from one publisher), it does not close it. **`sub` is not validated**: only the application knows what a subject may be. |
| §3.9 Use and Validate Audience | **covered** | `VerifierConfig.Audience` must appear in `aud`. Not required by default, because a single-recipient deployment legitimately mints without one — set it and it is enforced. |
| §3.10 Do Not Trust Received Claims | **partial** | `kid` is used only as a map key into a caller-supplied `jwk.Set`; it never reaches a query, a path or a URL. **`jku` and `x5u` are ignored entirely** — this package fetches nothing, so there is no SSRF surface to whitelist. Sanitising claim VALUES before the application uses them is out of scope, and is stated as such. |
| §3.11 Use Explicit Typing | **covered** | `IssuerConfig.Type` stamps a `typ`; `VerifierConfig.RequireType` demands one. The default is `"JWT"`. |
| §3.12 Mutually Exclusive Validation Rules | **covered (as a mechanism)** | `RequireType` + `Issuer` + `Audience` are the three levers §3.12 lists. Whether a deployment USES them is the deployment's decision; this package makes each of them one field. |

**Sections deliberately NOT addressed**, and why:

- **§2.3 / §3.6 — encryption (JWE).** There is no JWE here at all. A signed
  token is not confidential and this package never pretends otherwise; putting
  a secret in a claim puts it in a base64 string anybody can read.
- **§2.4 — plaintext leakage through ciphertext length.** Same reason.
- **§2.5 — insecure elliptic-curve *encryption* (ECDH-ES).** No key agreement
  happens here.
- **Replay.** `jti` is decoded and returned; nothing here remembers it. A
  replay cache is state with a lifetime and an eviction policy, which belongs
  to whoever owns the store — `pkg/v1/data/cache` is one answer. Key it on
  `jti`, never on the token string: an ES256 token from another issuer has two
  valid spellings (see §Do NOT).
- **Revocation.** Same: this package answers "is this token authentic and
  in-window", never "is it still wanted". `MaxLifetime` is the mitigation it
  CAN offer — a short window is what a recipient has instead of revocation.
- **`sub` semantics, and claim-value sanitisation** (§3.8, §3.10) — see the
  table. Both need vocabulary this package does not have.

## One refusal set — the claims reader

`decodeClaims` used to be `json.Unmarshal` into a `map[string]json.RawMessage`,
behind a duplicate-member pass over `json.Decoder.Token` — 95.5 % of an HS256
verification. It is now `readClaims`: one strict RFC 8259 pass with no
reflection, shared by `Verify` on every format, PASETO, the set verifier and
`VerifyInto`. Every refusal the legacy reader made is kept, in its order:
UTF-8, depth, then (one scan) malformed text, `DuplicateMember`, the member
cap (`TooLarge`), then the registered claims — null, a non-string `iss`/`sub`/
`jti`, a shape-illegal `aud`, a NumericDate that is NaN, Inf, past 1e11 or the
zero instant — then the private names (`ClaimNameInvalid`, `TooLarge`). Claims
are still read only AFTER the signature; base64url is still strict.

The legacy reader also refused, without saying so, every number a float64
cannot hold — `json.Decoder.Token` reads each number as one — at any depth,
header included; the scanner keeps that refusal (`checkMagnitude`). The RFC
8259 number grammar is checked before any number reaches `strconv`.

**Deliberate divergences** — inputs the legacy reader accepted and the scanner
refuses. They are the only ones `legacy_differential_test.go` admits; a
refusal outside them fails the oracle, and an input the legacy reader refused
and the scanner accepts fails it always:

| Class | Example | Legacy | Scanner |
|---|---|---|---|
| escaped top-level member name | `{"\u0065xp":1}` | read as `exp` | `MALFORMED` — a second spelling of a name is how a duplicate slips past |
| lone surrogate escape, anywhere | `"\ud800"`, `"\udbff"` | both U+FFFD: one string | `MALFORMED` |
| `null` inside an `aud` array | `{"aud":["a",null]}` | `["a",""]` | `MALFORMED` |
| JOSE header past 71 members | — | no bound | `TOO_LARGE` |

When both refuse, the codes are equal, or differ as the written precedence
permits (`precedenceAllows`): the scanner may answer `MALFORMED` for an input
carrying a listed class (it refuses the class where it meets it); `TOO_LARGE`
where the legacy reader found a duplicate or an empty name past the member cap
(duplicates are not searched past it); and `CLAIM_NAME_INVALID` and
`TOO_LARGE` may swap, since the legacy reader walked the private claims in map
order. `VerifyInto` adds one refusal of its own, `CLAIMS_BUF_OVERFLOW`, after
every other.

## Cost

Measured in `BENCH.md` — **median of nine samples across three separate
processes** on an 8-core AMD EPYC 7351P VM, `go1.27.1`. `realistic` is nine
claim members (iss/sub/aud/exp/iat/jti plus three private); `minimal` is the
three an issuer stamps on its own.

| Verify (per authenticated request) | minimal | realistic |
|---|---:|---:|
| **HS256** | **14 353 ns** | **28 905 ns** · 7 122 B · 121 allocs |
| PASETO v4.public | 97 873 ns | 112 154 ns · 5 834 B · 93 allocs |
| EdDSA | 104 459 ns | 119 406 ns · 6 610 B · 114 allocs |
| ES256 | 126 140 ns | 142 029 ns · 7 796 B · 134 allocs |

Issuing costs **2.1–2.4× less** than verifying, on all four. The table is the
legacy reader's (§1–§15 of `BENCH.md`); since the scanner (§17) HS256 realistic
allocates **29** times instead of 121, every format gains likewise, and the
time fell about 3.5× on an M1 under heavy load — re-measure before quoting it.
`Verify` still allocates on every call (the `ClaimsValue` is built of strings
and a map). **`VerifyInto` allocates nothing**, held race-off by
`verify_into_alloc_internal_test.go` (a hand-written gate: kit v0.1.0-rc.15
cannot carry the budget — the port is designed in core, whose fixture may not
import this layer, and the facade's alias is not a designed symbol).

**A JWK Set verification costs what a single-key one costs, plus the `kid`
header.** `NewSetVerifier` derives every member's binding ONCE at construction,
so selecting a key from a set is a map read over a slice the constructor already
built — measured at **exactly 0 additional allocations** with the token held
constant.

| Verify against a JWK Set (realistic claims) | ns | allocs | surcharge over single-key |
|---|---:|---:|---:|
| **HS256** | **29 846** | 126 | +717 ns · +5 allocs |
| EdDSA | 119 894 | 119 | +194 ns · +5 allocs |
| ES256 | 142 713 | 139 | +845 ns · +5 allocs |

All three surcharges are **+5 allocations and +95–96 B, identically** — the `kid`
member's own parsing cost, which any token carrying one pays. Before 2026-09 the
ES256 row read **+55 allocations**: `Verify` re-derived the key per token, and
for an EC key that meant `x509.MarshalPKIXPublicKey` immediately re-parsed by
`x509.ParsePKIXPublicKey`, at **8.3 % of the CPU and 28.2 % of the allocated
objects**. Building the verifier now costs **8 434 ns per key**, once per JWKS
refresh: break-even is under one request for a one-key set and **44 requests**
for a sixty-four-key one. `BENCH.md` §16 has the before/after tables, the
profiles and the reconciliation.

`jwk.Set.AllByKid`'s O(n) scan was measured too and is **refuted as a cost**:
**10.12 ns per set member** and one 176 B allocation, i.e. 0.53 % of a JWK Set
verification's allocations and half a percent of its latency. See `internal/service/crypto/key/jwk/BENCH.md` §4.

**"Is verification mostly crypto?" has two opposite answers.** HS256 is
**4.5 % signature, 95.5 % this package**; every asymmetric option is
**74–79 % signature** (up to 90.5 % on a small claim set). An optimisation to
the JSON path moves HS256 by roughly twenty times what it moves ES256 — which
is why the split is published per algorithm and never averaged.

Six extra claim members cost **+14.3–15.9 µs and exactly +4 038 B / +57
allocations**, identically through all three JWS algorithms. Decoding private
claims is **O(n²)** in their count, because `attachPrivate` adds them one at a
time through `core/security/token`'s copy-on-write `WithPrivateRaw`: 64 of them cost
205 µs and 191 KB. `MaxPrivateClaims` is what bounds that at 7× a realistic
payload; the fix needs a batch setter in `core/security/token`.

### Refusing is cheaper than accepting — all thirteen ways

| | ns | % of an accepted HS256 token |
|---|---:|---:|
| wrong segment count | **99.3** | 0.34 % |
| oversized (8 KiB+1, 1 MiB, 8 MiB — identical) | 258 | 0.89 % |
| not strict base64url | 335 | 1.16 % |
| header too deep | 528 | 1.83 % |
| duplicate header member | 1 396 | 4.83 % |
| `alg:none` / `alg` ≠ binding | 4 674 / 4 977 | 16.2 % / 17.2 % |
| bad signature | 6 239 | 21.6 % |
| claims too deep / duplicate claim | 7 170 / 7 361 | 24.8 % / 25.5 % |
| expired | 27 830 | 96.3 % |

**No refusal costs more than the acceptance it replaces**, so none is a
denial-of-service lever. The expired row is 96 % because an expired token is
parsed, verified and decoded in full first — §Ordering being visible in the
numbers — and reaching it costs an attacker a valid signature.

**The cost ordering IS the check ordering**, which is ADR 0042 §D4 verified by
measurement rather than by reading: every check costs strictly more than the
one before it and sits strictly after it. And the oversized refusal is **flat
at 168.5–170.2 ns across a 8 192× size range**, which is the CVE-2025-30204
bound doing its job.

### What the security properties cost

`checkHeader` — the algorithm-confusion gate this whole package is built
around — costs **15.6 ns, 0.05 %** of a verification. There has never been a
performance argument for reading the algorithm from the token, and now there is
a number saying so.

The expensive one is RFC 8725 §2.6: the duplicate-member refusal costs
**24.8 %** across its two calls, **5.5× the signature**, 26.9 % of the CPU
profile and 31.7 % of the allocated bytes. Three optimisations that would
reduce it are **refused by name in BENCH.md §13**, each with the property it
would weaken — merging the three JSON passes, pooling the `json.Decoder`, and
hand-decoding the registered string claims. All three replace a delegated
parser with a hand-rolled one on the path whose job is that two readers cannot
disagree about a token.

## PASETO — why only v4.public

`v4.local` is **not implemented, and cannot be here.** It needs XChaCha20 and
BLAKE2b, neither of which is in the Go standard library; the SDK's only
XChaCha lives in `third-party/x-crypto/xchacha`, over `golang.org/x/crypto`,
precisely so `internal/service` stays dep-light (ADR 0013). Pulling it in for
one token purpose would put `x/crypto` in every `pkg/v1` consumer's build.

So `v4.local` is refused by NAME — `SchemeUnsupported` with the scheme in a
field — rather than as "malformed", because an operator seeing a v4.local token
has a missing feature, not a corrupt one. If it ever ships it belongs under
`third-party/`, on the `hcl` codec precedent (ADR 0022/0034).

Two more PASETO decisions:

- **The footer must be the expected one.** The `Verifier` port returns claims,
  not a footer, so an unconstrained footer would be data this package
  authenticated and then dropped. `PasetoVerifierConfig.Footer` is compared
  with `subtle.ConstantTimeCompare`; empty means "the token must carry none".
  It is also bounded: a verifier refuses a decoded footer past `maxFooterLen`
  (1 KiB) as `TOO_LARGE` before reading the signature, so BOTH constructors
  refuse a longer configured footer (`POLICY_MISCONFIGURED`) — an issuer with
  one would mint only tokens the SDK refuses, and a verifier with one could
  never match anything. An empty footer is spelled by OMITTING the fourth
  segment, as the issuer writes it; a present but empty fourth segment
  (`<token>.`) is a second spelling of the same token and is `MALFORMED`
  (#438, `TestPasetoEmptyFooterHasOneSpelling`).
- **The configs are FLAT, not embedded.** `PasetoIssuerConfig` has no `Type`
  and no `KeyID` because PASETO has no header to put them in. Embedding
  `IssuerConfig` would have published two knobs this format silently ignores.
