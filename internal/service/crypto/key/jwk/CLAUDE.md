<!-- updated: 2026-10-03T12:00:00Z -->
# internal/service/crypto/key/jwk/

## Purpose

The **key FORMAT** for the crypto domain: RFC 7517 JSON Web Key and JWK Set,
over the key types the SDK already signs and tags with — **EC** (NIST
P-256/P-384/P-521), **OKP** (Ed25519) and **oct** (symmetric). Parse a JWK,
render one, select from a set by `kid`, and cross to the encodings
`ecdsasig` / `ed25519sig` / `hmacsha2` already speak.

**Stdlib-only.** A JWK is JSON plus base64url plus curve arithmetic, and
`encoding/json`, `encoding/base64`, `crypto/ecdh`, `crypto/ed25519` and
`crypto/x509` supply all three. No dependency is added to `internal/service`.

It is a **format, not a connector**: nothing here fetches a JWKS over HTTP,
caches one, honours `Cache-Control`, or follows an OpenID discovery document.
Those are transport and policy concerns with their own failure modes (retries,
TTLs, SSRF against a caller-supplied URL) and they belong to whatever later
package owns them — see **Do NOT**.

Code range: `0.3.42.*`, declared in `internal/core/crypto/key/jwk` — the core
path that mirrors this one (ADR 0160) — and imported here as `corejwk`. This
package returns those sentinels and declares none.

Public facade: `pkg/v1/security/token`, which aliases `KeyValue` as `JWK` and `Set` as
`JWKSet`, wraps `Parse` / `ParseSet` / `NewSet` as `ParseJWK` / `ParseJWKSet` /
`NewJWKSet`, and re-exports the six parse refusals (`.1`–`.6`) as `CodeJWK*`
codes and `JWK*` sentinels.

## The decision this package exists to make

`core/crypto.Key` is redacting **by design** (ADR 0013): `String` / `GoString`
/ `Format` answer `<redacted>`, and `Bytes()` is the single, deliberate way out. A JWK
serialiser is, definitionally, a function that turns that protected material
into JSON somebody will write to a file or an HTTP response. So the two
directions are **two differently named methods**, never one method with a flag:

| Call | Emits | Notes |
|---|---|---|
| `MarshalJSON` (i.e. plain `json.Marshal`) | public members | delegates to `MarshalPublic` — the path the language takes on its own is the safe one |
| `MarshalPublic` | public members | refuses an `oct` key with `NoPublicForm` |
| `MarshalPrivate` | public members **+ `d` / `k`** | the only path that emits key material, and it reads as such at the call site |

A symmetric key has **no public half** — its `k` member *is* the secret — so
`MarshalPublic` / `MarshalJSON` / `Public()` all refuse it rather than emit a
key-shaped object with no key in it. That is ADR 0030's rule applied to a key
format: the default a caller reaches for before understanding the question must
not be the dangerous one.

The same discipline covers the *other* leak surface. `KeyValue` and `Set`
implement `String` / `GoString` so `%v`, `%s` and `%#v` print kty / crv / kid
and a `private:` boolean, never an octet. Without `Set.String`, `fmt` walks the
unexported members reflectively and dumps every scalar — that is not a
hypothetical, it is what `TestFormattingNeverPrintsMaterial` catches when the
methods are removed. `KeyValue` also implements `Format`, so `%d` — which
bypasses `String` — prints the same metadata, and its private member sits
behind a pointer (`privateMember`), so a `KeyValue` in an UNEXPORTED field,
which `fmt` walks without calling any method, prints an address where the
secret octets would be (#433, `TestFormattingNeverPrintsMaterialThroughAnyVerb`).

## Contents

| File | Surface |
|---|---|
| `jwk.go` | `Type` (EC/OKP/oct), `Curve` (P-256/384/521, Ed25519), the immutable `KeyValue` + accessors (`Kty`/`Crv`/`Kid`/`Use`/`Alg`/`KeyOps`/`IsZero`/`IsPrivate`), `With*` copy-on-write setters, `Public`, `Equal`, redacting `String`/`GoString`/`Format`; the private member behind a pointer (`privateMember`, `holdPrivate`, `secret`) |
| `parse.go` | `Parse` — the single decode entry point, plus the per-family validators |
| `marshal.go` | `MarshalPublic` / `MarshalPrivate` / `MarshalJSON`, RFC 7638 `Thumbprint` and `WithThumbprintKid` — the thumbprint input is the RFC 8785 canonical form of the required members, `codec/jcs.Marshal` (#895), the same bytes as RFC 7638 §3.2's concatenation (`TestThumbprintInputIsJCS`); about eight times the cost of that concatenation (`BENCH.md`) |
| `bridge.go` | `FromECDSAPublic`/`FromECDSAPrivate`/`FromEd25519Public`/`FromEd25519Private`/`FromSecret` and the reverse `ECDSAPublic`/`ECDSAPrivate`/`Ed25519Public`/`Ed25519Private`/`Secret` |
| `curve.go` | curve tables (`coordLen`, `ecdhCurve`, `ellipticCurve`, `curveFromElliptic`) and the two real checks: `checkECPoint`, `checkECScalar` |
| `set.go` | `Set`, `NewSet`, `ParseSet`, `Keys`, `Len`, `ByKid`, `AllByKid`, the three set marshallers, redacting `String`/`GoString` |
| `wire.go` | `keyJSON` / `setJSON` (the only json-tagged structs) and the strict base64url codec |

The codes and sentinels — `Malformed` (.1), `MissingMember` (.2),
`UnsupportedKeyType` (.3), `UnsupportedCurve` (.4), `InvalidEncoding` (.5),
`KeyMismatch` (.6), `NoPublicForm` (.7), `NoPrivateMaterial` (.8),
`TypeMismatch` (.9), `KeyNotFound` (.10), `AmbiguousKid` (.11) — are
`internal/core/crypto/key/jwk`'s `gen_codes.go`, written by kit gen from
`design/crypto/key/jwk.yaml` (ADR 0164, ADR 0181).

## Key types

| `kty` | `crv` | Public | Private | SDK scheme behind it |
|---|---|---|---|---|
| `EC` | `P-256` | `x`, `y` | `d` | `service/crypto/sign/ecdsasig` (`ecdsa-p256`, JOSE `ES256`) |
| `EC` | `P-384`, `P-521` | `x`, `y` | `d` | **none** — representable, not signable here |
| `OKP` | `Ed25519` | `x` | `d` (32-octet **seed**) | `service/crypto/sign/ed25519sig` (JOSE `EdDSA`) |
| `oct` | — | *none* | `k` | `service/crypto/mac/hmacsha2` (JOSE `HS256`) |

**RSA is deliberately absent.** The SDK registers no RSA signer (ADR 0014 §Out
of scope), so an `RSA` JWK would promise an interop the crypto domain cannot
honour; it is rejected with `UnsupportedKeyType` rather than parsed into a key
nothing can use.

**P-384 / P-521 are the opposite call.** The SDK cannot sign with them, but a
JWK Set published by an identity provider routinely carries them, and refusing
to *represent* a key the format defines would push ad-hoc parsing back onto the
caller. They parse, validate, thumbprint and round-trip; only `crypto.Sign`
has nothing registered for them.

## Validation — what Parse actually checks

1. `kty` present, and one of `EC` / `OKP` / `oct`.
2. `crv` valid **for that `kty`** — `Ed25519` under `EC`, `P-256` under `OKP`,
   or any `crv` at all on an `oct` key, are each `UnsupportedCurve`.
3. Every member is **unpadded** base64url (RFC 7515 §2). Padding and the
   standard `+` / `/` alphabet are refused, not accommodated: a second valid
   spelling would let one key present two thumbprints.
4. EC coordinates and the OKP key are at the curve's **fixed** length
   (RFC 7518 §6.2.1.2, RFC 8037 §2) — leading zero octets carry meaning, so a
   "compact" 31-octet P-256 coordinate is a different number, not a tidier one.
   P-521 is 66 octets, not 65.
5. The point is **on** the declared curve, via `ecdh.Curve.NewPublicKey` (which
   also rejects the identity). A parser that only measures lengths accepts an
   off-curve point, and using one is a documented route to key recovery.
6. When `d` is present it is range-checked **and** must derive exactly the
   declared `x`/`y` (`ecdh.Curve.NewPrivateKey` + comparison; for OKP,
   `ed25519.NewKeyFromSeed(d)[32:] == x`). A JWK whose halves disagree is
   corrupt, or an attempt to make a signer and a verifier hold different keys.

Members this package does not model are **ignored** on the way in (RFC 7517 §4)
and **not** carried through on the way out: re-emitting a member we never
understood would be vouching for it. Round-tripping is therefore *semantic*;
it is octet-exact only for documents this package emitted.

## Do NOT

- **Do not add remote JWKS retrieval here** (HTTP fetch, cache, TTL, OpenID
  discovery). That is a connector, with its own failure modes — SSRF on a
  caller-supplied URL, retry/backoff, staleness policy — and none of them are
  format concerns. Out of scope for this package, deliberately.
- Do not add a `MarshalJSON`-style flag or option that emits `d` / `k`. The
  private path has a name; that is the whole design.
- Do not implement `UnmarshalJSON` — see above; it would break the receiver set
  that makes the safe default reachable.
- Do not log the result of `ECDSAPrivate` / `Ed25519Private` / `Secret`. They
  hand back live material exactly as `crypto.Key.Bytes()` does, and carry the
  same rule: straight to the scheme, never to a log line.
- Do not relax the strict base64url decode, the fixed coordinate length, or the
  `d`↔`x`/`y` consistency check to accept a lenient producer. Every one of them
  is a test row.
- Do not mint JWS/JWE/JWT here. This package models a KEY, not a token.

## Verification

```sh
bazel test --config=race //internal/service/crypto/key/jwk:jwk_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./crypto/key/jwk/...
```

Test vectors are published ones — RFC 7515 §A.3.1 (private ES256 key),
RFC 7517 §A.1 (public P-256 key), RFC 8037 §A.1/§A.3 (Ed25519 keypair and its
thumbprint) — so a regression in the on-curve or scalar-consistency checks
fails here instead of being masked by material this package generated itself.

`jwk_bench_test.go` reuses those same vectors. Its `benchKid` helper renders
FIXED-WIDTH kids, and that is load-bearing rather than tidy: Go compares strings
by length first, so a ragged `"k0"`…`"k63"` scheme made a two-octet lookup fail
on length against fifty-four of sixty-four members and measured `match=last`
43 % slower than `match=first` — on a scan that has no early exit and therefore
cannot produce that difference. `BENCH.md` §9 records the discarded row-set.

## Linter exemptions

Two scoped entries in `.ktn-linter.yaml`, both justified there:

- `KTN-VAR-BIGSTRUCT` — `KeyValue` is 176 B and passed by value on purpose. The
  value receiver is what makes `MarshalJSON` and the `String` redaction fire on
  a plain (non-pointer) key; a pointer method set would make both reachable only
  by callers who already knew to take an address. Same precedent as
  `core/net.IdentityValue`.
- `KTN-STRUCT-CTOR` — there is no from-parts constructor by design. Every
  `KeyValue` comes out of `Parse` or a `From*` bridge, each of which validates
  the material; a `NewKeyValue(kty, crv, x, y, d)` would be a second way in that
  skips all of it.

## Reference in NOTES.md

Moved unchanged to `internal/service/crypto/key/jwk/NOTES.md`, under the same titles, and read only by a change that needs its mechanics: `Ambiguous `kid` — the policy`; `Cost`; `Why there is no `UnmarshalJSON``.
