<!-- updated: 2026-10-24T00:00:00Z -->
# internal/service/security/token/

> Reference moved to `internal/service/security/token/NOTES.md` (ADR 0253), under the same section titles: The decision this package exists to make, Contents, RFC 8725 coverage — what is addressed, and what is not, One refusal set — the claims reader, Cost, PASETO — why only v4.public.

## Purpose

The two concrete security-token formats behind the `core/security/token` ports: **JWT
over JWS Compact Serialization** (RFC 7519 + RFC 7515) and **PASETO v4.public**.
Stdlib-only, composing the SDK's own crypto schemes rather than reimplementing
them — HMAC-SHA-256 from `service/crypto/mac/hmacsha2`, Ed25519 from
`service/crypto/sign/ed25519sig`, key material from `service/crypto/key/jwk`. No
dependency is added to `internal/service`.

Codes: none declared here. The domain verdicts a caller matches on
(`0.2.13.*`) and what is specific to the two formats — header parameters,
key-set selection, PASETO framing (`0.3.44.*`, allocated here — ADR 0042) — are
both declared in `core/security/token` (ADR 0160) and only raised here; the
`0.3.44.*` values were kept when their declarations moved.

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
  run **before** the scanner descends a level.
- `scanObject` indexes at most `maxClaimMembers` (71) members into a fixed
  array, and looks for duplicates only among those: the comparison is bounded
  by the cap, never by what the attacker sent.
- `MaxPrivateClaims` (in `core/security/token`) caps the member count.
- `NewSetVerifier` caps how many candidate keys one `kid` may cost.

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
  the refusal of an empty PASETO footer segment, or the duplicate-member
  refusal to accommodate a lenient producer. Each is a test row, and each
  exists so one token has exactly one spelling.
- Do not refuse a high-S ES256 signature on VERIFY, and do not key a
  revocation or replay list on the token string. ECDSA's (r, s) verifies as
  (r, n−s) does, so anyone holding a token can compute its twin without the
  key. `es256Signing.sign` writes s ≤ n/2, so a token THIS SDK signs has one
  spelling; but RFC 7518 §3.4 does not require low S, and most producers —
  crypto/ecdsa included — emit a high s about half the time, so the verifier,
  which serves external issuers through JWKS, accepts both. A list of revoked
  or seen tokens is keyed on `jti`, or on the claims, never on the string
  (#438, `TestES256SignsLowSAndVerifiesBoth`).
- Do not let a present time claim decode to the zero `time.Time`: that is
  "absent" to `ClaimsValue`, and an `exp` at 0001-01-01T00:00:00Z would read as
  no expiry at all. `decodeTimeClaim` refuses it as `MALFORMED` (#439).
- Do not let the set verifier reduce a header's fault to `SIGNATURE_INVALID`.
  `alg: none` is refused before any key is selected, and when no candidate
  accepts the header the first candidate's header verdict is returned —
  `ALGORITHM_MISMATCH`, `HEADER_UNSUPPORTED` — as the single-key verifier
  returns it (#440, `TestSetVerifierNamesTheHeaderVerdict`).
- Do not reach for `bytes.Equal` on a tag. The only secret comparison in this
  package is `hmacsha2.MAC.Verify`, which is `hmac.Equal`.
- Do not fetch a JWKS here. Same rule as `service/crypto/key/jwk`: retrieval is a
  connector with its own failure modes (SSRF, TTL, retries), and none of them
  are token concerns.
- Do not declare an error code here — an `errs.Define` or a `Code*` constant.
  Every code of the domain is declared in `internal/core/security/token`, at
  this package's path in the core (ADR 0160); a new format-specific refusal
  gets its code and sentinel there, in the range this package already owns
  (`0.3.44.*`). A `WrapParams` built here to wrap a stdlib cause names a core
  code, as `es256.go` does with `CodeIssueFailed`.

## Verification

```sh
bazel test --config=race //internal/service/security/token:token_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./security/token/...
```

`TestPreAuthEncodeMatchesTheSpecVectors` checks PAE against the three worked
examples in the PASETO specification rather than against this implementation —
an off-by-one in a length prefix produces signatures that verify perfectly
against themselves and against nobody else's.

Three allocation guards live under `//go:build !race` — two in
`split_alloc_internal_test.go` and one in `jwkbind_alloc_internal_test.go` — so
the race suite does not compile them and
`//internal/service/security/token:token_test` in `tools/alloc-lane-targets.txt` is
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

Two scoped entries in `.ktn-linter.yaml`, both justified there and both the
same class already granted to `service/crypto/key/jwk` and `core/net`:

- `KTN-VAR-BIGSTRUCT` — `ClaimsValue` (152 B) and the four config structs are
  passed by value because immutability and redaction are the point.
- `KTN-API-MINIF` — parameters typed by a contract (`ClaimsValue`,
  `jwk.KeyValue`, `crypto.Key`, `Algorithm`) cannot be narrowed to a
  one-method interface without breaking the contract.
