# internal/service/token/

## Purpose

The two concrete security-token formats behind the `core/token` ports: **JWT
over JWS Compact Serialization** (RFC 7519 + RFC 7515) and **PASETO v4.public**.
Stdlib-only, composing the SDK's own crypto schemes rather than reimplementing
them — HMAC-SHA-256 from `service/crypto/hmacsha2`, Ed25519 from
`service/crypto/ed25519sig`, key material from `service/crypto/jwk`. No
dependency is added to `internal/service`.

Code range: `0.3.44.*` (ADR 0042). The domain verdicts a caller matches on live
in `core/token` (`0.2.13.*`); this block holds only what is specific to the two
formats — header parameters, key-set selection, PASETO framing.

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
| `token.go` | package doc + the shared bounds (`DefaultMaxTokenLen`, `MaxTokenLenCeiling`, `DefaultMaxClaimDepth`, `MaxKeyCandidatesCeiling`, `MaxLeeway`) |
| `issuer_config.go` / `verifier_config.go` | `IssuerConfig` / `VerifierConfig` |
| `paseto_issuer_config.go` / `paseto_verifier_config.go` | the FLAT PASETO configs + their projections onto the shared policy |
| `policy.go` / `bounds_value.go` | `policyValue` — the validated, defaults-applied verification policy |
| `validate.go` | the shared post-authentication claim checks + `stampIssuedClaims` |
| `encoding.go` / `depth_scan.go` | bounded segment split, strict base64url, JSON depth + duplicate-member checks, PASETO `PAE` |
| `jsonstring.go` | `quoteJSONString` / `quoteJSONStrings` — JSON string rendering with no error channel |
| `claims_codec.go` | the shared claims traversal + the `claimShape` contract + the issue-side UTF-8 refusal (`checkClaimText`, `putPrivate`) + the verify-side refusals of text that is not UTF-8 and of a registered claim sent as JSON null (`nullRegisteredClaim` — `encoding/json` would decode `"aud": null` as an audience of `[""]` and `"nbf": null` as 1970; a private claim may still be null) |
| `jose_shape.go` / `paseto_shape.go` | the two claim-value encodings (NumericDate vs RFC 3339) |
| `jws.go` / `jws_parts.go` | `headerValue`, the JOSE header parse, `checkHeader` (the algorithm gate), `jwsPartsValue` |
| `jws_issuer.go` / `jws_verifier.go` | the JWS issuer and single-key verifier |
| `jwt.go` | the six JWT constructors |
| `keys.go` + `hs256_binding.go` / `es256_*.go` / `ed25519_*.go` | the algorithm-bound key contracts and their five implementations |
| `paseto.go` / `paseto_issuer.go` / `paseto_verifier.go` | PASETO v4.public |
| `jwkbridge.go` | `NewVerifierFromJWK`, `NewSetVerifier`, `setVerifier`, `boundKeyValue` + `indexByKid` (the set is bound ONCE, at construction — see §Cost) + `selectable` (a set no token could verify against is refused `POLICY_MISCONFIGURED`) |
| `token_compliance.go` | the compile-time contract assertions |
| `codes.go` / `errors.go` | `Code*` + `HeaderUnsupported` (.1), `KeyNotFound` (.2), `KeyIDMissing` (.3), `KeyIDAmbiguous` (.4), `FooterMismatch` (.5), `SchemeUnsupported` (.6), `DuplicateMember` (.7) |

## RFC 8725 coverage — what is addressed, and what is not

| Section | Status | How |
|---|---|---|
| §3.1 Perform Algorithm Verification | **covered** | The algorithm is bound at construction and the header is only ever compared to it. There is no way to express "accept whatever the token says". |
| §3.2 Use Appropriate Algorithms | **covered** | Closed set: HS256, ES256, EdDSA, PASETO v4.public. `none` has no representation in the `Algorithm` enum. Agility is a new constructor, not a config string. |
| §3.3 Validate All Cryptographic Operations | **covered** | One verdict, whole-token: a failed signature returns `SignatureInvalid` and the ZERO claim set. There is no partial-acceptance path. |
| §3.4 Validate Cryptographic Inputs | **covered** | EC public keys are validated as curve points via `(*ecdsa.PublicKey).ECDH()`, not merely measured; an ES256 private key's scalar must be present, in range, and derive exactly its declared public point (go1.27's `ecdsa` dereferences a nil `D` on the first `Sign`, and signs with a mismatched one without comparing); Ed25519 key lengths are checked before the stdlib would panic; ES256 signatures must be exactly 64 octets. |
| §3.5 Sufficient Key Entropy | **partial** | The LENGTH half is structural: HS256 takes a `core/crypto.Key`, fixed at 256 bits, so a short passphrase cannot become one. The ENTROPY half is **not measurable here** — a 32-byte key of ASCII is still 32 bytes. Derive one (`pkg/v1/kdf`); do not type one. |
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
  to whoever owns the store — `pkg/v1/cache` is one answer.
- **Revocation.** Same: this package answers "is this token authentic and
  in-window", never "is it still wanted". `MaxLifetime` is the mitigation it
  CAN offer — a short window is what a recipient has instead of revocation.
- **`sub` semantics, and claim-value sanitisation** (§3.8, §3.10) — see the
  table. Both need vocabulary this package does not have.

## Bounds — the CVE-2025-30204 class

Every function that touches an unauthenticated token is `O(len(input))` with a
bound checked FIRST and no allocation proportional to a count the attacker
chooses.

- `splitCompact` checks `MaxTokenLen` **before** scanning, then walks with
  `strings.IndexByte` into a fixed `[4]string` array. The parts are views into
  the caller's string. There is no `strings.Split` anywhere in this package —
  that call, on a token of a few megabytes of `.`, is what CVE-2025-30204 was.
- `decodeSegment` checks `DecodedLen` before decoding.
- `checkJSONDepth` is a linear, string-aware scan with three fields of state,
  run **before** `encoding/json` builds a frame per level.
- `MaxPrivateClaims` (in `core/token`) caps the member count.
- `NewSetVerifier` caps how many candidate keys one `kid` may cost.

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

Issuing costs **2.1–2.4× less** than verifying, on all four. `Verify`
allocates on every call; there is no zero-allocation claim here to hold.

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
verification's allocations and half a percent of its latency. See `internal/service/crypto/jwk/BENCH.md` §4.

**"Is verification mostly crypto?" has two opposite answers.** HS256 is
**4.5 % signature, 95.5 % this package**; every asymmetric option is
**74–79 % signature** (up to 90.5 % on a small claim set). An optimisation to
the JSON path moves HS256 by roughly twenty times what it moves ES256 — which
is why the split is published per algorithm and never averaged.

Six extra claim members cost **+14.3–15.9 µs and exactly +4 038 B / +57
allocations**, identically through all three JWS algorithms. Decoding private
claims is **O(n²)** in their count, because `attachPrivate` adds them one at a
time through `core/token`'s copy-on-write `WithPrivateRaw`: 64 of them cost
205 µs and 191 KB. `MaxPrivateClaims` is what bounds that at 7× a realistic
payload; the fix needs a batch setter in `core/token`.

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

## Ordering — signature first, always

`Verify` is: parse → compare the algorithm → verify the signature → decode and
judge the claims. Reporting "expired" for a token whose signature was never
checked tells an attacker what is inside a forgery, and hands the application a
claim set out of one. `TestSignatureIsCheckedBeforeClaims` pins it.

## Errors do not carry their stdlib cause

`malformed()` and `keyUnsuitable()` wrap the SENTINEL, not the underlying
error, which is the opposite of this repository's usual habit. Two reasons,
both deliberate:

1. A stdlib parse error quotes what it choked on — `time.ParseError` echoes the
   timestamp, `json.SyntaxError` gives an offset, `base64.CorruptInputError` a
   position. Here those bytes came off an **unauthenticated** token, so keeping
   the cause reachable would put token content one `errors.Unwrap` from any log
   line that walks a chain.
2. `Wrap`'s origin-wins path inherits the sentinel's HTTP status and exit code;
   the stdlib-cause path cannot, and would silently answer 500 for a refused
   credential. Several of the jwk causes are themselves `*errs.Error`, where
   origin-wins would make the RESULT carry `0.3.42.*` instead of this domain's
   `KeyUnsuitable`.

The one exception is `es256Signing.sign`: an `ecdsa.Sign` entropy fault is
about this host, not about anything an attacker sent, so its cause is carried.

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
  never match anything.
- **The configs are FLAT, not embedded.** `PasetoIssuerConfig` has no `Type`
  and no `KeyID` because PASETO has no header to put them in. Embedding
  `IssuerConfig` would have published two knobs this format silently ignores.

## Do NOT

- **Do not add an algorithm parameter to any constructor**, and do not add a
  registry keyed on `alg`. The header is attacker-controlled; a lookup keyed on
  it is algorithm confusion with extra steps. This is ADR 0042's whole subject.
- **Do not accept `none`.** Not behind a flag, not behind a build tag, not "for
  tests". The `Algorithm` enum has no value that spells it, and
  `TestNoAlgorithmSpellsNone` scans the whole `uint8` range to keep it that way.
- Do not report a claim verdict before the signature verifies.
- Do not attach a stdlib parse error to a token-parsing verdict — see above.
- Do not log a token, a key, a claim value or a `kid`. Every `Public` and
  `Private` string here names a CHECK, never a value; `ClaimsValue` renders its
  shape and not its contents.
- Do not relax the strict base64url decoding, the exact ES256 signature length,
  or the duplicate-member refusal to accommodate a lenient producer. Each is a
  test row, and each exists so one token has exactly one spelling.
- Do not reach for `bytes.Equal` on a tag. The only secret comparison in this
  package is `hmacsha2.MAC.Verify`, which is `hmac.Equal`.
- Do not fetch a JWKS here. Same rule as `service/crypto/jwk`: retrieval is a
  connector with its own failure modes (SSRF, TTL, retries), and none of them
  are token concerns.

## Verification

```sh
bazel test --config=race //internal/service/token:token_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./token/...
```

`TestPreAuthEncodeMatchesTheSpecVectors` checks PAE against the three worked
examples in the PASETO specification rather than against this implementation —
an off-by-one in a length prefix produces signatures that verify perfectly
against themselves and against nobody else's.

Three allocation guards live under `//go:build !race` — two in
`split_alloc_internal_test.go` and one in `jwkbind_alloc_internal_test.go` — so
the race suite does not compile them and
`//internal/service/token:token_test` in `tools/alloc-lane-targets.txt` is
their ONLY lane (rule 12). They pin the two claims §Bounds makes and nothing
enforced before 2026-09: `TestSplitCompactAllocatesNothing` (a well-formed
split allocates zero — a regression to `strings.Split` returns identical parts
and is invisible to every functional test here) and
`TestOversizedRefusalDoesNotScanTheToken` (the length bound is checked before
the scan). Each carries its mutation and the observed failure in its doc
comment. The second is a WALL-TIME assertion, deliberately: the first version
counted allocations and the bound-ordering mutation passed against it, because
the walk is `strings.IndexByte` into a fixed array and scanning eight megabytes
allocates nothing. Its input is separator-FREE for a related reason — a token
of megabytes of `.` is short-circuited three bytes in by the segment-count
check, so it never exercises the length bound at all.

`TestSetVerificationDoesNotRederiveTheKey` is the third: it counts a JWKS
verification's allocations against a single-key verification of the **same
token** and budgets the delta, which is 0. Its mutation — the key derivation
put back inside the candidate loop — reports `49.0 per call`. Two non-`!race`
guards ship with it and both are in the race suite:
`TestUnverifiableCandidatesStillCountAgainstTheBound` (a member the SDK cannot
verify with still counts against `MaxKeyCandidates`, because the bound counts
what the publisher listed — its mutation makes a token that must be refused
verify, and it is the ONLY test in the package that catches it) and
`TestABindRefusalIsNotANilInterface` (`bindECJWK` returns `bindP256Public`'s
VALUE type through an interface result, so a refused binding is not a nil
interface — which is why `boundKeyValue` carries a `usable` boolean). Each
carries its mutation and the observed failure in its doc comment.

Four refusals at construction or at `Issue`, each paired with a test of the
accepting side so an over-eager fix cannot pass unnoticed, and each carrying
its mutations and their observed failures in its doc comment:
`TestIssueRefusesClaimTextThatIsNotUTF8` / `TestMultiByteTextRoundTripsExactly`
/ `TestIssuerConfigTextIsRefusedAtConstruction` (§3.7 on the issue path),
`TestASetThatCanVerifyNothingIsRefused` (a JWK Set of only kid-less or unusable
members — the unusable members of an ACCEPTED set still count against
`MaxKeyCandidates`), `TestAPrivateKeyThatIsNotOneKeyIsRefused` /
`TestEveryWayToObtainAP256KeyStillIssues` (an ES256 scalar that is nil, out of
range, or another key's), and `TestPasetoFooterBoundIsOneNumberOnBothSides` (a
footer exactly at the bound verifies; one octet past it is refused on both
sides).

## Linter exemptions

Three scoped entries in `.ktn-linter.yaml`, all justified there and all the
same class already granted to `service/crypto/jwk` and `core/net`:

- `KTN-VAR-BIGSTRUCT` — `ClaimsValue` (152 B) and the four config structs are
  passed by value because immutability and redaction are the point.
- `KTN-API-MINIF` — parameters typed by a contract (`ClaimsValue`,
  `jwk.KeyValue`, `crypto.Key`, `Algorithm`) cannot be narrowed to a
  one-method interface without breaking the contract.
