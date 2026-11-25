<!-- updated: 2026-10-05T00:00:00Z -->
# framework/internal/service/entitlement/

## Purpose

The engine behind `framework/entitlement` (ADR 0079): fetch a vendor-signed roster,
authenticate it, decide whether this machine is entitled, and keep deciding when
the network is gone. Implements `framework/internal/core/entitlement`'s contract; consumers
import the facade, never this package.

Every vendor-specific fact — origins, cache directory, OIDC audience, enrolment
URL — lives in `ProductValue`, which is why one implementation serves every
product (ADR 0078 §1).

It was the SDK library's `internal/service/entitlement` until ADR 0158 made it
the framework's. Its own range `0.3.67.*` kept its value (ADR 0160), and it now
reaches the SDK only through `pkg/v1/*` and `internal/kernel/errs`
(framework/CLAUDE.md rule 1): the cache lock is `pkg/v1/app/lock`'s file locker.

## Contents

| File | Role |
|---|---|
| `service.go` | the `Service` handle, `Verify`, the origin fallback, `matchSubject` |
| `anchors.go` | the ORDERED list of vendor keys this verifier accepts, its bound, and the two readers |
| `roster_parse.go` | `ParseRoster` — two documents, raw + detached signature |
| `crypto.go` | the crypto domain's operations, asked through `pkg/v1/crypto/sign` and `pkg/v1/crypto/hash` (ADR 0158 §2): `verifiedBy`, the package's one ed25519 check (roster, bundle, Roughtime); `payloadDigest`, the SHA-256 a mark compares; `sha512Prefix`, a Roughtime Merkle node. RS256 is not here — see `oidc.go` |
| `bundle.go` | `ParseBundle` — the one-document form the cache stores |
| `cache.go` | the offline copy and the anti-rollback ratchet, over storage AND acceptance; `DefaultCacheDir`, `WithCache` |
| `cache_lock.go` | exclusion over the cache directory — what rename does not give; `holdCacheForWrite` skips a write it cannot guard |
| `cache_lock_unix.go` / `cache_lock_windows.go` | `holdCacheForRead`: no exclusion where rename is atomic for a reader, the write's exclusion on Windows |
| `ci.go` / `ciseat.go` | GitHub Actions OIDC: mint, verify, then look up the seat |
| `jsonnames.go` | `checkNoDuplicateNames` — a document naming one member twice, at any depth, is refused by the roster, bundle, JWKS and token decoders |
| `jwks.go` | the issuer's published RSA keys |
| `oidc.go` | the token's claim set and its strict decoding |
| `roughtime*.go` | signed network time — advisory, fail-open, servers ship empty |
| `version.go` | the version floor a roster can mandate |
| `product.go` | `ProductValue`, `Label`, `PublishedOrigins`, `Validate` |
| `gen_codes.go` | the one code this implementation owns, `0.3.67.*`, and its sentinel — written by kit gen from `design/framework/entitlement.yaml` (ADR 0164) |
| `wrap.go` | `refuse` / `classify` / `classifyForeign` / `annotate`, plus `diagnose` |

## How it works

This prose was the package's hand-written README. The README is now
written by `tools/genindex` from `docs/api`, as every framework package's is
(framework/CLAUDE.md rule 3), so it lives here.

### The CI seat is bounded by its own token

A CI grant's `NotAfter` includes the Actions token's `exp`, so a seat cannot
outlive the proof that established it. That is a CHANGE IN OBSERVABLE BEHAVIOUR:
the bound was the roster's window, up to 24 h, and it is now at most the token's
own window — 30 min (`maxTokenLifetime`) in the widest case GitHub mints, and in
practice the few minutes a real Actions token carries. A long-running process on
a runner that aged against a CI grant for hours now has to re-verify inside that
window.

No clock skew is added to the bound. `clockSkew` (2 min) exists to ADMIT a token
whose clock disagrees slightly, which is the permissive direction and the right
one there; the same allowance on a bound would extend the grant past the proof.
The two together are what makes the exposure of a leaked token statable: one free
seat PER VERIFIER for up to 32 minutes — a short token bounds the duration a
stolen proof works for, never the number of processes that can present it.

### Errors

Fourteen sentinels, all `errs.Define`-typed in `framework/internal/core/entitlement`,
range `0.2.35.*`. `errors.Is` and `errs.HasCode` both work through the
wrapping. A fifteenth code, `CodeProductInvalid`, has no sentinel: `Validate`
mints one error per failure with `errs.Wrap`, because the caller needs to know
WHICH assertion the product failed.

| Class | Sentinels | What a caller should do |
|---|---|---|
| cannot decide | `ErrRosterUnreachable` | keep serving, tell the operator |
| refused | `ErrRevoked`, `ErrLicenseExpired`, `ErrKeyMismatch`, `ErrNoPossession` | stop |
| not enrolled | `ErrNoLicense`, `ErrAmbiguousLicense` | enrol, or disambiguate |
| CI | `ErrCIUnverifiable`, `ErrCIUnknownKey`, `ErrCINotEntitled` | fall back to a device seat |
| tamper | `ErrRosterUnsigned`, `ErrClockRegressed` | do not retry |
| other | `ErrRosterStale`, `ErrUpdateRequired` | refresh, or upgrade |
| construction | `CodeProductInvalid` (no sentinel) | fix the product's origin list |

### Identity

`NewService` takes a `coreent.Identity` — three methods, none of which says
"ssh". The ssh implementation is `framework/connectors/ssh.NewSSHIdentity(dir)`
and lives in a module of its own because `golang.org/x/crypto/ssh` reaches
`golang.org/x/sys` (ADR 0079, ADR 0158).

## Known debt

A cache refresh can still be refused by a file holder outside this process —
antivirus, backup, a search indexer on Windows. `holdCacheForWrite` and
`holdCacheForRead` exclude every holder that takes the same lock, which is
every one this SDK controls, and no
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
  `framework/connectors/ssh` for the reason ADR 0079 measures.
- Import anything under `internal/` but `internal/kernel/errs`. A mechanism this
  package needs and `pkg/v1` lacks is published there first (ADR 0147 §2, ADR
  0158 §2).
- Collapse `RosterUnreachable` into a refusal. It says "cannot decide", and
  reporting an outage as a revocation is the one wrong answer.
- Swap `jsonnames.go` for `strictjson`, or the roster and mint clients for
  `pkg/v1/net/client`, while the reasons in "Where ADR 0158 §2 is not followed"
  still hold: the first refuses a genuine Actions token and any roster with a
  field this build predates, the second every customer behind a proxy.
- Read the anchor list anywhere but `parseBundleAnyAnchor` / `bundleMarkAnyAnchor`,
  or let a roster field, an environment variable or the cache add to it. The list
  being a BUILD decision is the whole of what bounds the surface it costs, and a
  document signed by one listed key cannot be what decides which keys are listed.
- Give the empty list a code of its own. It refuses under `ErrRosterUnsigned`
  with its own `condition`, for the reason the sixteenth code was refused.
- Make `markCeiling` discard a mark instead of refusing on it, or move the
  ceiling into `rosterMark` where there is no clock. Both are the same bypass.
- Apply the anti-replay comparison to `cachedRoster`, or describe the guarantee
  as unconditional. Two different mistakes, both of them a sentence that outruns
  the code.
- Add a JWKS cache, or a refresh keyed on an unknown `kid`, without reading the
  three tests named above first. The absence is the mechanism: it is what makes a
  rotation a non-event, and the refresh is the repair for the staleness only a
  cache introduces.
- Restore either retired claim about refreshing the key set. A sentinel or a
  comment that advertises a remedy nobody implements is the defect this sweep
  closed, not a wording preference.
- Write a doc comment describing a POLICY for a claim nothing reads.
  `event_name` and `runner_environment` carried one for months, four lines under
  a type comment promising the opposite;
  `Test_VerifyActionsToken_ignoresTheClaimsNothingReads` is what keeps the
  silence mechanical now.

## Verification

```sh
bazel test --config=race //framework/internal/service/entitlement:entitlement_test
(cd framework && GOWORK=off go test -race ./internal/service/entitlement/...)
```

The suite also runs on real kernels in `e2e-cross.yml` (`./internal/service/entitlement`
is in `FRAMEWORK_PKGS`, run from `framework/`), illumos and Solaris included since
ADR 0144. The FIFO test
creates its pipe through `makeFifo`: `syscall.Mkfifo` where the stdlib has it
(`fifo_mkfifo_internal_test.go`), `syscall.Mknod` of an `S_IFIFO` node on
illumos and Solaris, whose stdlib has no `Mkfifo`
(`fifo_mknod_internal_test.go`).

## Reference in NOTES.md

Moved unchanged to `framework/internal/service/entitlement/NOTES.md`, under the same titles, and read only by a change that needs its mechanics: `The verification flow`; `Offline`; `Why-this-shape`; `Where ADR 0158 §2 is not followed, and why`.
