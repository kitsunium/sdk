# ADR 0158 — distribution mechanisms are the framework's, not the SDK's

- **Status**: Accepted
- **Date**: 2026-10-03
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0076](0076-what-a-branch-changed-is-a-value-that-can-say-it-does-not-know.md) (the `vcs` domain), [ADR 0077](0077-a-self-update-is-an-order-of-operations-and-a-product-name-is-not-part-of-it.md) (`selfupdate`), [ADR 0079](0079-the-entitlement-split-x-sys-was-never-in-the-mechanism.md) (`entitlement`'s core, service and facade, and its ssh identity), [ADR 0080](0080-the-gate-decides-and-performs-nothing.md) (`gate`), [ADR 0100](0100-a-program-reads-what-it-was-built-from-and-asks-git-only-about-a-working-tree.md) (`git.Head` — `process.Build` and `process.Self` stay), [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) (what the framework holds, and its connectors), [ADR 0150](0150-a-release-names-its-tag-keys-rotate-and-a-replacement-answers-before-it-stands.md) (`selfupdate`'s options)
- **Related**: [ADR 0078](0078-entitlement-is-quarantined-because-ssh-brings-x-sys.md), [ADR 0087](0087-the-root-a-caller-named-is-a-spelling-it-did-not-choose.md), [ADR 0091](0091-a-single-trust-anchor-is-a-key-with-no-way-out.md), [ADR 0092](0092-a-possession-proof-that-cannot-name-the-key-it-is-about.md), [ADR 0144](0144-illumos-and-solaris-supervise-on-their-own-kernels-word.md) (the domains as they were decided), [ADR 0155](0155-every-layer-groups-its-packages-by-family-and-a-path-may-move-while-v0.md) (no distribution family), [ADR 0156](0156-the-public-module-links-the-standard-library-and-nothing-else.md) / [ADR 0159](0159-the-kernel-holds-what-the-domains-rewrote-and-is-published-by-nature.md) (`semver`), [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md) (codes keep their values)

## Context

Four domains came into the SDK from one product's distribution tooling:

| Domain | What it answers | Production lines (core + service + facade, + connector) |
|---|---|---|
| `entitlement` | may this account use this product: a vendor-signed roster, an offline cache, an anti-rollback ratchet, a CI seat | 798 + 5 619 + 427, + 1 057 for the ssh identity under `third-party/` |
| `selfupdate` | replace this binary with a signed release | 291 + 2 856 + 424 |
| `gate` | is this invocation subject to the check, and what does a mandated upgrade do | 481 + 121 + 111 |
| `git` (`vcs`) | what a branch changed, and what a working tree is at | 166 + 1 703 + 197 |

No SDK domain imports any of them. The one importer outside their own trees is
the framework: `framework/internal/kit/build.go` reads `git.Head` to describe a
product's build. They are product logic — licensing, release delivery, the
policy around both — assembled from the SDK's mechanisms, which is what ADR
0147 calls a framework.

And they do not use the SDK's mechanisms. `entitlement` verifies ed25519
signatures with `crypto/ed25519` directly, carries a JWT verifier and a JWKS
parser of its own beside `token` and `crypto/jwk` — for RS256, an algorithm the
crypto domain deliberately ships no scheme for — detects duplicate JSON
members with its own walker beside `codec/strictjson`, builds bare
`http.Client` values beside the guarded `net/client`, and reads the wall clock
where every SDK wait takes an injected one; `selfupdate` repeats the
signature, the digest and the transport. Inside `internal/` nothing forces
them through the SDK's own domains, so nothing did.

## Decision

1. **`entitlement`, `selfupdate`, `gate` and `git` become packages of the
   framework module**, each a public package over an implementation under
   `framework/internal/`, the shape `framework/kit` already has. The `vcs`
   domain's port goes with its engine; it is named `git` there, as its facade
   always was.
2. **They import what the framework imports: `pkg/v1/*` and
   `internal/kernel/errs`** (ADR 0147 §2). So the deduplication is not
   optional: a signature is verified through the crypto domain's signing
   facade, a JSON document through `strictjson`, an HTTP request through the
   guarded client, a version through `semver` (ADR 0159), a lock through
   `pkg/v1`'s `lock`, a wait through `clock`. A mechanism they need that
   `pkg/v1` does not publish is published there first, in its domain, under
   its own ADR. The first such question is the CI seat's RS256 token: it is
   answered in the crypto domain, by an ADR, or not at all.
3. **The ssh identity becomes a connector module of the framework**, one
   vendor dependency (`x/crypto/ssh`, and the `x/sys` it brings) in one
   module, the shape ADR 0147 §7 gives the database drivers. ADR 0079's split
   stands: the mechanism is stdlib-only, the identity carries the vendor.
4. **Their error codes keep their values** (ADR 0160): the core ranges
   `0.2.33.*`–`0.2.36.*` and the service ranges `0.3.65.*`–`0.3.67.*` move with
   their declarations, `codeRangeOwners` points at the new directories, and the
   framework's own new codes stay in `0.4.*`.
5. **The four `pkg/v1` facades are removed** — `pkg/v1/entitlement`,
   `pkg/v1/selfupdate`, `pkg/v1/gate`, `pkg/v1/git` — under the v0 licence for
   import paths (ADR 0155 §4).
6. **What a program was built from stays in the SDK.** `process.Build`,
   `ParseBuild` and `process.Self` (ADR 0100) read the running program and are
   `proc`'s; only `git.Head`, which asks git about a working tree, moves.

## Consequences / Semantics

- **Implemented by the reorganisation series**, in the step that moves modules:
  the four packages and the connector in the framework, their rewrite onto
  `pkg/v1`, the codes re-pointed, the facades removed. This record changes no
  code.
- The SDK sheds four domains it carried for one kind of product — about
  13 000 production lines across core, service and facades, 14 000 with the
  ssh identity — and its families have no distribution group (ADR 0155).
- The ADRs these domains were decided in stay their records. What they
  decided — a signature checked before a digest, no key and no install, a
  value that degrades rather than answers empty — holds in the framework
  unchanged; only the layer they live in and what they may import change.
- The framework's own build description imports the framework's `git`.

## Breaking changes

`pkg/v1/entitlement`, `pkg/v1/selfupdate`, `pkg/v1/gate` and `pkg/v1/git`
disappear; a consumer imports the framework's packages instead and requires the
framework module — permitted while v0 (ADR 0155 §4). Error code values do not
change.

## Alternatives considered

- **Keep them in the SDK under a distribution family.** Nothing in the SDK
  needs them, and inside `internal/` nothing would force them through the
  domains they now duplicate.
- **An auxiliary module of their own, outside the framework.** It would need
  the same `pkg/v1`-only rule the framework already enforces, a release of its
  own and a place in every lane, for code whose one consumer is the framework.
- **Leave `git` in the SDK.** Its only consumer is the framework, and its
  changed set is a tool's question, not a service's.

## Deferred

None.

## References

- `framework/internal/kit/build.go` — the one importer outside the four
  domains' own trees.
- `internal/service/entitlement/{roster_parse,roughtime_verify,oidc,jwks,jsonnames,service,ci,roughtime}.go`,
  `internal/service/selfupdate/{keys,signature,transport,checksum}.go` — the
  re-implemented mechanisms.
