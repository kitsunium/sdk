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
| `cache_lock.go` | exclusion over the cache directory — what rename does not give |
| `ci.go` / `ciseat.go` | GitHub Actions OIDC: mint, verify, then look up the seat |
| `jwks.go` | the issuer's published RSA keys |
| `oidc.go` | the token's claim set and its strict decoding |
| `roughtime*.go` | signed network time — advisory, fail-open, servers ship empty |
| `version.go` | the version floor a roster can mandate |
| `product.go` | `ProductValue`, `Label`, `DefaultCacheDir`, `Validate` |
| `errors.go` | the one code this implementation owns, `0.3.67.*`, and its sentinel |
| `wrap.go` | `refuse` / `classify` / `annotate`, plus `diagnose` and `particulars` |

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
- **The ratchet is a compare-and-install, so it needs exclusion.** Reading the
  high-water mark and renaming a bundle over it are two filesystem operations.
  Without a lock between them two writers both read the same mark, both
  conclude they are newer, and whichever renames LAST sets it — measured at 183
  of 400 rounds, and what it loses is anti-rollback distance, since `checkClock`
  refuses a clock earlier than that mark. `holdCache` makes the pair one
  operation; `markWhileHeld` is the read a caller already holding it uses,
  because the guard is not reentrant.
- **Readers take the same guard, and only Windows needs them to.** A rename is
  atomic for a POSIX reader, so nothing there requires it. Windows refuses to
  replace a file another handle holds open, and `syscall.Open` never asks for
  `FILE_SHARE_DELETE` — so the cache's own reader, in a second process, is what
  makes a refresh fail. Sharing DELETE is not a way out: measured on
  `windows-latest`, `MoveFileEx` still refuses. A holder this package does not
  control — antivirus, backup, indexer — still can, and that residue is
  accepted and logged rather than retried past.
- **The public sentence names no particular, and that is the point.** Every
  refusal here is built with `refuse` or `classify` over one of the fifteen
  contract sentinels, so `err.Error()` is the wire-safe half and nothing else:
  no url, no host, no path, no subject, no kid. Where it happened travels in
  `Fields`, and `particulars` reads it back.
  `TestNoParticularReachesThePublicSentence` asserts both directions on six
  refusals, because leaking a particular and losing it are both defects and
  only one of them is the one everybody remembers.

- **`annotate` guards, and the guard is not defensive.** `errs.Wrap` has no
  spelling for "add a field, decide nothing": zero `WrapParams` over a cause
  carrying no `*errs.Error` returns `CodeInvalidWrapParams`. `Identity` is a
  PORT, so a consumer's plain `errors.New` reaches `ciContext` — and would have
  been replaced by "internal wrap failure" on the one path that reports it.

- **`readCappedFile` and `readBounded` exist because the inputs are hostile.**
  Everything here parses bytes fetched from the network or read from a cache an
  attacker may have written, before any signature has vouched for them.

- **The nil-tolerance contract covers the CONSTRUCTORS too, and it did not.**
  Every `ProductValue` accessor tolerates a nil receiver, and so does `Validate`
  — but `NewService` and `NewServiceWithGetter` read the `Origins` FIELD, which
  no method can guard, so `New(identity, vendor, nil)` panicked on exactly the
  path that runs when a consumer has configured nothing yet. Both now go through
  `PublishedOrigins()`. A product publishing nowhere yields a verifier that
  refuses with `RosterUnreachable`, which a caller can handle.

## Known debt

A cache refresh can still be refused by a file holder outside this process —
antivirus, backup, a search indexer on Windows. `holdCache` excludes every
holder that takes the same lock, which is every one this SDK controls, and no
lock reaches the others. The refusal is logged and the next invocation retries
it.

## Do NOT

- Build an error with `fmt.Errorf`. There were 112 of them and there are none;
  the three shapes in `wrap.go` are what a call site chooses between, and the
  package is in `//:audit_sources` so a code that strays out of `0.3.67.*`
  fails the build.
- Spell a cause as `cause.Error()`. After the conversion that is the wire-safe
  sentence and carries no url and no syscall text — `publishedJWKS` was one
  edit away from swapping its diagnosis for a tautology. Use `particulars`.
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
