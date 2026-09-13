# ADR 0079 — the entitlement split: x/sys was never in the mechanism, only in the identity

- **Status**: Accepted
- **Date**: 2026-09-12
- **Deciders**: SDK maintainers
- **Supersedes**: the Decision of [ADR 0078](0078-entitlement-is-quarantined-because-ssh-brings-x-sys.md) (its Context and its three inner decisions stand)
- **Related**: [ADR 0034](0034-hcl-quarantine-rationale-corrected.md) (measure what a dependency introduces), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (what a public alias may point at), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a refusal is not a degradation), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) (the code range), [ADR 0035](0035-pp-range-ownership-enforcement.md) (range ownership)

## Context

ADR 0078 put the whole entitlement domain under `third-party/` because
`golang.org/x/crypto/ssh` reaches `golang.org/x/sys`, which is banned SDK-wide.
It recorded the split as the better long-term shape and deliberately did not do
it, on the grounds that doing both at once would make the versement impossible
to diff against its source:

> **Split it: the stdlib-pure 92% into `pkg/v1`, the SSH identity into
> `third-party/`.** […] It is the better long-term shape and it is deliberately
> not what this change does: it doubles the work and would have split a
> versement across two module boundaries on its way in.

That reason has expired. The versement landed, it is on `main`, and its diff
against the source implementation is a matter of record. What remains is a
mechanism nobody can reach without also pulling the AWS SDK, ClickHouse, Mongo,
protobuf, redis, mysql and OpenTelemetry into their graph — for a check whose
whole purpose is to run in a single distributed binary.

The quarantine was also drawn around the wrong thing. Measured, not assumed:

| Reaches `x/crypto/ssh` | Lines (production) |
|---|---|
| no — roster, signature, cache, ratchet, CI seat, roughtime, version floor | 4 146 |
| yes — key parsing, fingerprint, possession, enrolment | 689 |

`x/sys` is in the IDENTITY, not in the mechanism. Quarantining the mechanism
with it was a boundary drawn along the package it happened to arrive in.

## Decision

**Split the domain along the four-layer contract, and quarantine only the ssh
implementation.**

```
internal/core/entitlement/     561 lines   the Identity port + the roster values + 15 codes
internal/service/entitlement/ 3436 lines   the engine: roster, cache, ratchet, CI seat, roughtime
pkg/v1/entitlement/            149 lines   the facade — aliases, New, 15 codes
third-party/entitlement/       689 lines   the ssh Identity implementation + enrolment
```

### 1. The seam is a three-method port, and none of them says "ssh"

```go
type Identity interface {
	Discover() (subject string, err error)
	Fingerprint(subject string) (fingerprint string, err error)
	ProvePossession(subject string) error
}
```

That is the entire surface the verification engine needs from the machine: which
subject it claims to be, what fingerprint the roster should hold for it, and that
the claim can be proven. Nothing about where key material lives, what format it
is in, or who may read it.

`ProvePossession` returns only an error because a nil error IS the proof.
Anything else returned here would be a second thing the caller had to verify.

### 2. `third-party/entitlement` becomes one implementation of that port

`SSHIdentity` wraps a key directory and implements the three methods over the
material the user already has, in OpenSSH format. Enrolment —
`GenerateKeyPair`, `IssueURL`, `NewSubjectID` — stays with it, because minting
an identity is the same policy as reading one.

Its compile-time proof lives in `sshidentity_compliance.go`, so a method added
to the port fails in the SDK's own build rather than at a consumer's.

### 3. The public API gains the mechanism and no dependency

Measured on the `pkg` module, `GOWORK=off`, before and after:

```
modules in pkg's graph @ HEAD:  8
modules in pkg's graph after:   8
difference: none
```

Zero. The service's only non-stdlib import is `golang.org/x/mod/semver`, for the
version floor a roster can mandate, and `x/mod` was already in that graph.
`internal/service/go.mod` and `pkg/go.mod` still contain no `x/sys` — checked,
not assumed.

## Consequences

- A consumer that manages its own key custody — a TPM, a cloud KMS, a hardware
  token, a key file of its own format — implements three methods and gets the
  roster, the offline cache, the anti-rollback ratchet, the CI seat and the
  version floor from `pkg/v1/entitlement`, inheriting none of the root module.
- A consumer that wants the ssh identity requires the root module and its graph,
  exactly as before. Nothing regressed for them; the alternative simply exists
  now.
- The domain claims code range `0.2.35.*` (`0x00_02_23_*`) in `internal/core`,
  vacating `0.3.65.*`. Hand-registered in `codeRangeOwners` and in
  `//:audit_sources`, per ADR 0035.
- `ProductValue` stays in `internal/service` and is aliased from `pkg/v1` as
  `Product`, per ADR 0074: it is one engine's construction parameters — origins,
  cache directory, OIDC audience, enrolment URL — not something a second
  implementation of the port would have to accept.
- Three copies of the product-name fallback ("entitlement" when a product names
  none) collapse into one exported `ProductValue.Label`. The source
  implementation carried two; the split would have made it three.

## Breaking changes

For anything importing `third-party/entitlement` by pseudo-version: `Service`,
`NewService`, `ParseRoster`, `ParseBundle`, `VerifyCI`, `ProductValue`, the
roster values and the fifteen `Code*`/`Err*` symbols moved. Their new homes are
`pkg/v1/entitlement` (public) or the internal layers behind it.

The root module is never tagged — `cut-tags.sh` chains
`internal/{kernel,core,service}` and `pkg` only — so `third-party/entitlement`
was reachable exclusively by pseudo-version, and only between ADR 0078 and this
record — both dated 2026-09-12, with no release cut in between.

`NewService` now takes a `coreent.Identity` where it took an ssh directory
string. A caller keeping the previous behaviour passes
`entitlement.NewSSHIdentity(dir)`.

## Alternatives considered

- **Leave it under `third-party/` and wait for a second consumer.** ADR 0078's
  own Deferred section says that is the trigger. The trigger arrived in the same
  week — ktn-linter needs the mechanism and already owns its ssh identity — and
  waiting would have meant migrating the linter onto a package it would then
  have had to be migrated off.
- **Move the ssh identity into `internal/service` behind a build tag.** A build
  tag cannot keep a module out of `go.mod`: the import is still there for
  `go mod tidy` to resolve, so `x/sys` would enter `pkg`'s graph for every
  consumer whether they compiled it or not.
- **Re-implement OpenSSH key parsing against the stdlib.** Unchanged from ADR
  0078: it is the one piece of this domain where a parsing bug is a security
  bug, and `x/crypto/ssh` is the reference implementation.
- **Put `Identity` in `internal/kernel`.** The kernel is stdlib-only AND
  generic. A port named after entitlement subjects and fingerprints is neither.

## Deferred

Carried forward unchanged from ADR 0078, none of them addressed here:

- **The frozen clock.** Every source of time an offline process can read belongs
  to the party being checked. The ratchet raises the cost; it does not close the
  hole.
- **`RoughtimeServers` ships EMPTY.** The mechanism is implemented and tested;
  naming a server list would make every consumer depend on a host the SDK does
  not operate.
- ~~**`fmt.Errorf` in the service.** The engine came across with the source
  implementation's error construction, which predates SDK rule 2. The fifteen
  sentinels are `errs.Define`-typed and the wrapping uses `%w`, so `errors.Is`
  and `errs.HasCode` both work through it; the conversion is a separate change
  that touches every refusal path and deserves its own diff.~~ **CLOSED.** The
  separate change is [PR #195](https://github.com/kitsunium/sdk/pull/195):
  **112 sites in `internal/service/entitlement` and 25 in
  `third-party/entitlement`, both now zero.** Three things it established are
  worth carrying here rather than leaving in a merged diff.

  - **What was actually missing was the Public/Private split, not the call.**
    The entry above is correct that `errors.Is` and `errs.HasCode` worked
    through the old construction — and that was the reason it looked cheap to
    defer. What did not work is the half those two say nothing about: every
    refusal's `Error()` carried a publication url, a GitHub host, a JWKS key
    id, a subject, a cache path and whatever the transport or the filesystem
    said. The public half is the one documented safe for a response body, so
    the debt was a leak and not a style violation. It is now a sentence and
    nothing else, with the particulars in `Fields`, asserted in BOTH directions
    by `TestNoParticularReachesThePublicSentence` in each package.

  - **User-visible error text changed at every converted site**, and anything
    parsing those strings breaks. Three tests in the tree were parsing them.

  - **`errs.Wrap` has no spelling for "annotate without classifying".** Zero
    `WrapParams` over a cause carrying no `*errs.Error` fails
    `validateDefineArgs` and returns `CodeInvalidWrapParams` — "internal wrap
    failure" — with the caller's own error demoted to a cause. `Identity` is a
    PORT, so a consumer implementing it with a plain `errors.New` reaches that
    path, and two call sites here add a field to a refusal somebody else owns.
    Both packages carry an `annotate` helper that guards on
    `errors.AsType[*errs.Error]` and returns such an error untouched. A kernel
    affordance for this — a Wrap that adds fields and decides nothing — would
    remove the guard from both copies and is NOT proposed here: it is a
    `internal/kernel/errs` decision for the whole repository rather than for
    one domain.

  `internal/service/entitlement` re-enters `//:audit_sources` with a range of
  its own, `0.3.67.*`, for the one failure the contract has no word for — a
  cache that could not be written. `third-party/entitlement` finally declares a
  code in the `0.3.65.*` range `codeRangeOwners` has recorded for it since
  ADR 0078: `ENROLMENT_FAILED`, for the six ways minting a key pair can fail,
  none of which the three-method port describes.
- ~~**The cache's rename on Windows, and `rememberRoster`'s lost update.** Both
  are recorded on the versement's review threads and neither is reachable from
  this CI matrix.~~ **CLOSED.** The second clause expired:
  [PR #190](https://github.com/kitsunium/sdk/pull/190) proved a
  Windows test executes on `windows-latest` through `e2e-cross.yml`, and
  `./entitlement` joining `SERVICE_PKGS` is the whole of what made it reachable.
  Both defects were reproduced there and fixed; what was measured in the process
  contradicts part of what the threads recorded, so it is written down here
  rather than left in a closed conversation.

  - **The rename is not the defect; a concurrent READER is.** The thread called
    `os.Rename` over an existing destination "an unsupported replacement
    operation on Windows". It is not. go1.27's
    `internal/syscall/windows.Rename` is
    `MoveFileEx(from, to, MOVEFILE_REPLACE_EXISTING)`, and asserted on
    `windows-latest` with nobody holding the destination, it succeeds. What
    Windows refuses is replacing a destination another handle holds open —
    and `syscall.Open` asks for `FILE_SHARE_READ|FILE_SHARE_WRITE` and never
    `FILE_SHARE_DELETE`, so every file Go opens is such a destination. The
    holder in practice is this package's own `readCappedFile` in a second
    process. The severity recorded on the thread — "after the initial cache
    write, later online verification silently retains the stale bundle" — was
    therefore overstated: an uncontended refresh has always worked there.
  - **`FILE_SHARE_DELETE` on the read path is not a fix.** Measured, because
    it would have been the cheap one: a destination held with
    `FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE` still fails with
    `Access is denied.` Replacing a name is not unlinking it. That left
    exclusion as the only answer, which is why the fix is a lock and not a
    share mode.
  - **The lost update is real and it is the ratchet that loses.** 183 of 400
    rounds on the Linux runner ended with the high-water mark BELOW the newest
    generation offered. `checkClock` refuses a clock reading earlier than that
    mark, so what a lost update loses is anti-rollback distance — not
    idempotent content.
  - **The same missing exclusion also destroyed the cache, which no review
    caught.** The staging name carried `os.Getpid()`, so two goroutines in one
    process shared a staging file and installed the interleaving of two
    bundles: 14 of 400 rounds left bytes that authenticate as nothing where a
    valid cache had been. `os.CreateTemp` replaced it.
  - **Windows also refuses two writers racing ONE name, and that one is not
    fixed because it is not broken.** Two unguarded installs onto the same
    destination collided in 99 of 400 rounds there and in 0 on every Unix lane,
    because MoveFileEx must delete the destination to replace it while
    rename(2) replaces unconditionally. The cache stayed valid in all 400, and
    production never installs unguarded, so the assertion that both writers
    also SUCCEED is made only where the kernel promises it.
  - **Still open, and deliberately.** A holder this package does not control —
    an antivirus scanner, a backup agent, a search indexer — can hold the
    bundle open and make a refresh fail exactly as before. No lock reaches
    them; only a retry would, and a retry cannot be falsified in this CI
    matrix. The failure is logged, and the next invocation refreshes.

## References

- [ADR 0078](0078-entitlement-is-quarantined-because-ssh-brings-x-sys.md) — the quarantine this splits, and the Context that still holds
- [ADR 0034](0034-hcl-quarantine-rationale-corrected.md) — the probe method used to measure both boundaries
- [ADR 0074](0074-what-a-public-alias-may-point-at.md) — why `Product` and `Service` alias the service layer
- ed25519 — RFC 8032, https://www.rfc-editor.org/rfc/rfc8032
