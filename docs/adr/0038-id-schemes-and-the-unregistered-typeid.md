# ADR 0038 — NanoID, KSUID, TypeID: a Scheme that is deliberately not in the registry

- **Status**: Accepted
- **Date**: 2026-09-09
- **Deciders**: SDK maintainers
- **Related**: [ADR 0024](0024-sdk-id-domain.md) (the `id` domain and its `Scheme` registry), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero values), [ADR 0035](0035-pp-range-ownership-enforcement.md) (the `0.3.39` block these use)
- **Extends**: ADR 0024 §Registry — adds three schemes and one documented exception to it

## Context

ADR 0024 established `id` with UUIDv4, UUIDv7, ULID and snowflake behind a
`Scheme` registry, where `New(scheme)` resolves a name to a generator. Three
more schemes were added: NanoID, KSUID and TypeID, all stdlib.

Two of them are ordinary registry entries. The third breaks the registry's
implicit invariant, and that is what this ADR is for: **every `Scheme` had so
far been reachable through `New`.** TypeID is not, and the reason generalises.

## Decision

1. **NanoID and KSUID register normally** (`coreid.Register`), alongside the
   existing four.

2. **TypeID is deliberately NOT registered.** `New("typeid")` returns
   `UNKNOWN_SCHEME`; the generator is reachable only through
   `NewTypeID(prefix)`.

   A TypeID is `<prefix>_<uuidv7-base32>`, and **the prefix names the caller's
   entity** — `user_`, `invoice_`, `order_`. A registered singleton would have
   to either invent that vocabulary or mint a prefixless identifier. The second
   is the silent degradation ADR 0031 forbids: the caller asks for a TypeID and
   receives something that is not one, with no error. The first is worse — the
   SDK deciding what the application's entities are called.

   `TypeIDScheme` stays exported, with a doc warning, because
   `Generator.Scheme()` reports it. A test pins the absence as intentional, so
   a future contributor "fixing" the gap fails a named assertion rather than
   silently re-introducing the degradation.

3. **NanoID splits the ADR 0031 line on purpose.** The zero-value struct
   resolves to 21 — the format's *published* default, not one the SDK invented —
   while `NewNanoID(0)` is refused. That is the field-versus-constructor
   distinction ADR 0031 already draws for `NewTimeout`: a zero field is an
   absent opinion, a zero constructor argument is a stated one.

4. **NanoID never uses modulo.** `randomFromAlphabet` masks each random byte to
   the smallest `2^k-1` spanning the alphabet and **rejects** any masked value
   past the last symbol, redrawing that position. A naive `%` skews roughly 21%
   over a 62-symbol alphabet.

   The shipped 64-symbol alphabet makes the mask exactly 6 bits, so **nothing is
   ever rejected on the production path** — which would leave the rejection
   branch untested in place. The tests therefore drive the sampler with 62-,
   33- and 3-symbol alphabets (≈3%, ≈48%, 25% rejection) and assert uniformity
   to within 15%; the naive-modulo skew sits about 7σ from that band, so the
   test separates the two mechanisms without flaking.

5. **A KSUID minted outside the 32-bit second window is refused**
   (`ID_TIMESTAMP_RANGE`, `0.3.39.7`), not wrapped. Wrapping would silently
   break the sortability the format exists for — the identifiers would still
   look valid and would order wrongly.

## Consequences

- Codes `0.3.39.4`–`.7` (`ID_INVALID_SIZE`, `ID_INVALID_PREFIX`, `ID_MALFORMED`,
  `ID_TIMESTAMP_RANGE`) land in the block `internal/service/id` already owns, so
  `codeRangeOwners` is untouched (ADR 0035 §Decision 4, just-in-time allocation).
- **The `Scheme` registry is no longer exhaustive.** Anything enumerating
  schemes — documentation, a future `AvailableSchemes()` — must say whether it
  lists registered generators or all schemes. This ADR is the reason the two
  can differ.
- Consumers get type-carrying identifiers, which removes a class of
  cross-entity id confusion at the boundary where identifiers arrive from
  outside.

## Why not

- **Register TypeID with an empty or placeholder prefix.** Rejected: see
  Decision 2. It is the exact failure ADR 0031 was written about.
- **Take the prefix from a package-level default.** Rejected: process-global
  state that changes what an identifier *means*, invisible at the call site.
- **Keep TypeID out of the SDK entirely.** Rejected: the generator is useful and
  correct; only its *registration* is impossible. Dropping it would discard
  working code to preserve a tidier invariant.
- **Test NanoID's rejection branch by shipping a non-power-of-two alphabet.**
  Rejected: changing production behaviour to make a test reachable. Driving the
  sampler directly is the honest way.

## Breaking changes

None. Three schemes and four codes are added in the block `internal/service/id`
already owns; no registered scheme, code or signature changes. The one semantic
change — the `Scheme` registry is no longer exhaustive — is a documentation
contract (§Consequences), not an API one.

## Deferred

- **An `AvailableSchemes()` enumeration.** Named in §Consequences only to state
  what it would have to say — registered generators, or every scheme. Nothing
  needs it today, so it is not built.

## References

- `internal/service/id/CLAUDE.md`, `internal/service/id/{nanoid,ksuid,typeid}.go`
- ADR 0024 (the domain), ADR 0031 (zero values), ADR 0035 (range ownership)
