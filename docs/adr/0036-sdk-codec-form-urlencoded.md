# ADR 0036 — form-urlencoded: repetition is the only array syntax, and the round-trip says so

- **Status**: Accepted
- **Date**: 2026-09-09
- **Deciders**: SDK maintainers
- **Related**: [ADR 0003](0003-sdk-codec-package.md) (universal codec package), [ADR 0021](0021-sdk-codec-bson.md) / [ADR 0022](0022-sdk-codec-hcl.md) (per-codec ADR precedent), [ADR 0031](0031-policy-zero-values-are-never-inert.md), [ADR 0035](0035-pp-range-ownership-enforcement.md) (the range this claims)

## Context

The codec registry shipped 22 Formats and not the one every HTML form sends.
`net/url` is stdlib, so the cost is zero dependencies — but the format itself is
under-specified in exactly the place that matters, and that is why it needs a
decision record rather than a quiet registration.

`application/x-www-form-urlencoded` does not define arrays. Every framework
invented its own: PHP and Rails read `a[]=`, `qs` reads `a[0]=`, ASP.NET
comma-joins, and none of them agree. A codec that picks one silently makes the
SDK speak a dialect its callers did not choose.

## Decision

1. **A key may repeat, and repetition is the only array syntax.** `a=1&a=2`
   decodes to `["1","2"]` in wire order. The native Go shape is therefore
   `url.Values` (`map[string][]string`).

2. **The dialects are refused, not supported.** `a[]=`, `a[0]=` and
   comma-joining are framework conventions the format does not define; comma
   joining is additionally ambiguous the moment a value contains a comma. A
   caller who needs one encodes it itself.

3. **Last-wins and first-wins are refused.** Both discard a value the wire
   actually carried *while reporting success* — the silent-degradation shape
   ADR 0031 rejects on a different surface.

4. **A repeated key decoded into `map[string]string` returns typed
   `MULTI_VALUE` (`0.3.40.3`)**, never a silent collapse. The reason is
   deliberately **not** listed in the facade's `isValueShapeMismatch`, so a
   fidelity refusal reaches the caller instead of being retried and relabelled
   `PROMOTE_FAILED`.

5. **Non-bijectivity is stated, not faked.**
   - `Unmarshal ∘ Marshal` is the identity on `url.Values` **except** a key
     bound to an empty slice: the wire has no way to say "present, zero values".
   - `Marshal ∘ Unmarshal` preserves *values* and canonicalises *bytes* — keys
     sorted (Go map order is randomised, so sorting is the only reproducible
     output), `a` becomes `a=`, `%2f` becomes `%2F`, a trailing `&` is dropped.
   - The tests pin each row and assert the **second** round-trip is a byte-level
     fixpoint. That is the honest contract: not "bytes survive", but "bytes
     converge after one pass".

6. **Struct targets go through the facade's JSON bridge**, producing a single
   `_json=…` pair. It is documented as a transport wrapper, not a field mapping,
   because no agreed convention exists for nesting a struct into this format.

## Consequences

- Registry counts move to 24 Formats over 16 in-tree codec packages, verified at
  runtime (`len(codec.Available())`), not inferred from a diff.
- The facade needed a promotion strategy (`wrapForFormat` / `containerForFormat`
  / `extractForm`), mirroring CSV: three registry-driven suites in
  `pkg/v1/codec` fail hard on any codec reaching `Available()` without one.
- **There is deliberately no `MarshalFailed` code.** Past the argument-shape
  gate, `url.Values.Encode()` cannot fail. Reserving a code for an unreachable
  branch would be a lie in the registry.

## Why not

- **Support one dialect behind an option.** Rejected: an option that changes
  wire semantics turns one Format name into several incompatible formats, and
  the registry keys on the name.
- **Collapse a repeated key into `map[string]string` by picking the first.**
  Rejected: see Decision 3. The refusal is the feature.
- **Claim byte-level bijectivity.** Rejected: it is false, and a codec that
  claims a round-trip it does not have is worse than one that documents the
  gap — the caller stops checking.

## Breaking changes

None. `form` is a new Format name; every existing Format, code and signature is
unchanged.

## Deferred

None. The `a[]=`, `a[0]=` and comma dialects are refused (§Why not), not
deferred: supporting one of them would turn one Format name into several
incompatible formats.

## References

- `internal/service/codec/form/CLAUDE.md` §The decision that matters
- ADR 0003 (codec contract), ADR 0035 (range `0.3.40` ownership)
