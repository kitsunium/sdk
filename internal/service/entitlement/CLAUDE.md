# internal/service/entitlement/

## Purpose

The engine behind `pkg/v1/entitlement` (ADR 0079): fetch a vendor-signed roster,
authenticate it, decide whether this machine is entitled, and keep deciding when
the network is gone. Implements `internal/core/entitlement`'s contract; consumers
import the facade, never this package.

Every vendor-specific fact — origins, cache directory, OIDC audience, enrolment
URL — lives in `ProductValue`, which is why one implementation serves every
product (ADR 0078 §1).

## Contents

| File | Role |
|---|---|
| `service.go` | the `Service` handle, `Verify`, the origin fallback, `matchSubject` |
| `roster_parse.go` | `ParseRoster` — two documents, raw + detached signature |
| `bundle.go` | `ParseBundle` — the one-document form the cache stores |
| `cache.go` | the offline copy and the anti-rollback ratchet |
| `ci.go` / `ciseat.go` | GitHub Actions OIDC: mint, verify, then look up the seat |
| `jwks.go` | the issuer's published RSA keys |
| `oidc.go` | the token's claim set and its strict decoding |
| `roughtime*.go` | signed network time — advisory, fail-open, servers ship empty |
| `version.go` | the version floor a roster can mandate |
| `product.go` | `ProductValue`, `Label`, `DefaultCacheDir`, `Validate` |

## Why-this-shape

- **The order in `Verify` is the security property.** Authenticate the roster
  against the vendor key, then check its window, then match the subject, then
  demand a possession proof. Every gate before the last one reads material the
  roster hands to everyone; only possession distinguishes the holder.
- **Offline is a fallback, never a bypass.** `cachedRoster` replays a bundle
  this machine already authenticated, and it re-runs the identical signature and
  window checks. What it cannot re-run is the clock, which is why the ratchet
  exists — and why the frozen-clock hole is documented as open rather than
  claimed closed.
- **`VerifyCI` takes the roster as a signed document, not as an interface.**
  GitHub's word is that the run is real, not that it is paid for. Entitlement is
  a property of the vendor's roster, and the party being checked must not be able
  to supply the type that answers it. See `.ktn-linter.yaml`'s KTN-API-MINIF
  entry for why the narrowing the linter suggests is refused here.
- **A CI failure is not a refusal unless the roster says so.** A runner that
  also holds a device key must keep working, so `ciSeat`'s failure falls through
  — except when `ciRefusalIsFinal`, which is the only place "this run must be CI"
  can be stated without letting the party being checked state it.
- **`readCappedFile` and `readBounded` exist because the inputs are hostile.**
  Everything here parses bytes fetched from the network or read from a cache an
  attacker may have written, before any signature has vouched for them.

## Known debt

`fmt.Errorf` throughout, against SDK rule 2. The engine came across with the
source implementation's error construction; the fifteen sentinels are
`errs.Define`-typed and the wrapping uses `%w`, so `errors.Is` and
`errs.HasCode` both work through it. ADR 0079 §Deferred.

## Do NOT

- Name a roughtime server here. The list is a deployment decision, and shipping
  one would make every consumer depend on a host the SDK does not operate.
- Add an ssh import. The identity is a port; its ssh implementation lives in
  `third-party/entitlement` for the reason ADR 0079 measures.
- Collapse `RosterUnreachable` into a refusal. It says "cannot decide", and
  reporting an outage as a revocation is the one wrong answer.

## Verification

```sh
bazel test --config=race //internal/service/entitlement:entitlement_test
```
