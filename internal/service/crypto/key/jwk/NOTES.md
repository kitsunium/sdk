# internal/service/crypto/key/jwk/ — reference

Moved unchanged from `internal/service/crypto/key/jwk/CLAUDE.md` (ADR 0253); each section keeps its title.

## Ambiguous `kid` — the policy

RFC 7517 §4.5 only *SHOULD*-s distinct `kid` values, so a set holding two keys
under one id is legal and does occur mid-rotation. Resolving it by taking the
first match would make the answer depend on JSON member order — an order no RFC
guarantees and no publisher promises to preserve.

- `ByKid(kid)` returns **the** key: `KeyNotFound` on zero matches,
  `AmbiguousKid` (with a `candidates` field) on two or more. It never picks.
- `AllByKid(kid)` is the **rotation path**: every candidate, in document order,
  for the caller to try in turn.
- `ByKid("")` / `AllByKid("")` resolve **nothing**. "Match the keys that carry
  no id" is never what a lookup by id means.
- `WithThumbprintKid` mints an RFC 7638 `kid` from the key itself, which is how
  a publisher avoids the ambiguity in the first place.

Same principle as ADR 0031: where any SDK-chosen answer would be arbitrary,
refuse rather than guess quietly.

**`AllByKid` is O(n) and stays O(n)**, deliberately. Measured at **10.12 ns per
set member** with one 176 B allocation for the match — 0.09 % of an ES256 token
verification at n=1 and 0.54 % at n=64 (`BENCH.md` §4). A `map[string][]KeyValue`
field on `Set` was refused for a reason that outranks the number: `Set` is an
**immutable value**, copied by every constructor and accessor, and a map field
is a reference two copies would share. A consumer that needs an index builds one
— `internal/service/security/token`'s set verifier does, at construction, because it
needs the derived binding grouped with it and that does not belong in a key
format. See `BENCH.md` §10.

## Cost

Measured in `BENCH.md` — median of nine samples across three separate
processes. The one number that matters to a caller:

| accessor | ns | B | allocs |
|---|---:|---:|---:|
| `ECDSAPublic()` (EC) | **3 870** | 1 288 | **30** |
| `Ed25519Public()` (OKP) | 42.9 | 32 | 1 |
| `Secret()` (oct) | 44.5 | 32 | 1 |

**The EC accessor costs 90× the other two**, because RFC 7518 §6.2.1 stores an
EC key as bare coordinates while `service/crypto/sign/ecdsasig` consumes PKIX, so
somebody has to run `x509.MarshalPKIXPublicKey` — and `encoding/asn1.Marshal`
walks a struct reflectively. That is not a defect here; it is the reason a
consumer must call it **once**, not per request.

It was being called per request. `internal/service/security/token`'s JWK Set verifier
rebuilt the key on every token — marshal to DER, then immediately
`x509.ParsePKIXPublicKey` the DER back — at **8.3 % of that verification's CPU
and 28.2 % of its allocated objects**. Fixed on the consumer's side in 2026-09;
nothing in this package changed. `BENCH.md` is the whole argument, with the
profiles verbatim.

`Parse` costs 2 857 ns (EC) / 1 388 (OKP) / 1 352 (oct); `ParseSet` is linear at
**3 690 ns per additional member** across a 16× range, so there is no hidden
quadratic in the set decoder.

## Why there is no `UnmarshalJSON`

`MarshalJSON` is load-bearing — it is what makes plain `json.Marshal` render the
**public** JWK. It only fires on a value receiver, so the whole method set stays
value-receiver, so there is no receiver left for `UnmarshalJSON`, which must be
a pointer to assign. `Parse` / `ParseSet` are the decode entry points, and
having exactly one keeps validation in exactly one place.

The visible consequence: decoding straight into a `KeyValue` field yields the
**zero value**, not a key. That is inert — every method refuses the zero value
with `MissingMember` — and it is pinned by
`TestParseIsTheOnlyDecodeEntryPoint` so it stays a documented property rather
than a surprise.

The same zero struct is why `Parse` and `ParseSet` test for JSON `null` first:
`json.Unmarshal` accepts `null` into a struct and leaves it zero, so a `null`
key, a `null` set document and a `null` member of `"keys"` each read as
`MissingMember`. None of them is an object at all, and each is now `Malformed`
— the code a caller routing on the refusal needs.
