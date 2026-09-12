# ADR 0077 — a self-update is an order of operations, and a product name is not part of it

- **Status**: Accepted
- **Date**: 2026-09-12
- **Deciders**: SDK maintainers
- **Related**: [ADR 0013](0013-sdk-crypto-domain.md) (the crypto domain the verification leans on), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a refusal is not a degradation), [ADR 0065](0065-sdk-cli-domain.md) (the `cli` domain whose binaries this updates), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (why `Source` and `Service` alias the service layer)

## Context

The SDK ships a `cli` domain, so it is used to build binaries that get
distributed. Every distributed binary eventually needs to replace itself, and
almost every implementation of that gets the security property wrong in the same
way: it downloads an archive, compares a SHA-256 against a checksum file fetched
from beside it, and calls that verified.

It is not verified. Whoever can substitute the archive can substitute the
checksum file next to it. The digest only proves anything once something has
vouched for the manifest — which means a signature, verified against a key that
did not arrive over the same channel.

So the hard part is an ORDER:

1. a detached signature over the checksum manifest, verified against a key
   linked into the build;
2. the archive's digest against that now-authenticated manifest;
3. only then does anything touch the disk.

Reversing 1 and 2 turns the chain into decoration while leaving every log line
looking identical.

`kodflow/ktn-linter`'s `pkg/updater` gets this right, and carries a suite that
asserts the order rather than documenting it. It also hard-codes, as package
constants: the release host account, two repository names, the archive asset
pattern, the inner binary name, and two environment variables. Those are
properties of one product's distribution, not of the update mechanism, and
together they are the only reason a correct implementation served exactly one
binary.

## Decision

**A `selfupdate` domain, whose contract is the order of operations, and whose
product-specific facts are a value the caller supplies.**

`internal/core/selfupdate` holds three ports (`Getter`, `FileSystem`, `Copier`),
two value types, and eighteen typed sentinels in range `0.2.34.*`.
`internal/service/selfupdate` implements it. `pkg/v1/selfupdate` is the facade.

### 1. No vendor key, no install

A `Service` built without `WithVendorKey` refuses every install with
`NoVendorKey`. The alternative — verify when a key is present, skip when it is
absent — fails OPEN: a build that lost its key installs anything, and the
security property silently becomes a property of a build flag nobody inspects.

This is the ADR 0031 shape applied to a credential rather than a policy: the
absent case is an explicit refusal, never an inert pass.

### 2. `SourceValue` carries everything about one product

Owner, stable repository, candidate repository, product name. The two
repositories may differ because a project whose source repository goes private
still has to serve already-installed binaries an update path — a public mirror
for stable releases, candidates left behind credentials the public binary does
not carry.

The environment-variable names are DERIVED from the product rather than
supplied, and the derivation is constrained by a promise the source
implementation had already made. Its suite pinned `KTN_LINTER_AUTO_UPGRADE` and
`KTN_LINTER_ALLOW_SUDO` against renaming, because "a renamed variable strands
every environment already setting it". Uppercase-and-fold-punctuation is the
rule that keeps that promise: `ktn-linter` → `KTN_LINTER`. Any other rule breaks
every CI image already setting them, which is why a test pins the mapping.

### 3. Eighteen codes, in three classes

A caller must tell apart what a retry fixes, what an opt-in fixes, and what
nothing fixes:

| Class | Codes | Advice |
|---|---|---|
| transient | `DownloadFailed`, `UnexpectedStatus` | retry |
| recoverable by opt-in | `ElevationNotAuthorised` | name the variable to set |
| supply chain | `SignatureMissing`, `SignatureInvalid`, `ChecksumMismatch`, `NoVendorKey` | **do not retry, do not install by hand** |

The codes are re-exported from `pkg/v1/selfupdate`, following `pkg/v1/authz`. A
facade that hid them would force a consumer to match on message text for a
distinction that is the difference between a flaky network and a substituted
release.

### 4. Two opt-ins, never one

Authorising an unattended upgrade must not also authorise privilege escalation.
They derive separately and a test pins that they never collide.

## Consequences

- A consumer distributing a binary gets the whole chain — signature, digest,
  extraction, atomic replacement, consent, escalation opt-in — without
  re-deriving the order that makes it worth anything.
- **`internal/service` gains `golang.org/x/mod/semver`.** It is used for three
  functions (`Compare`, `IsValid`, `Prerelease`), it is a `golang.org/x` module,
  and `go list -deps` shows it is stdlib-pure: no transitive module reaches the
  consumer graph. The alternative was re-implementing semver comparison,
  including prerelease ordering, which is a well-known source of subtle bugs for
  no dependency saving that anyone would notice.
- The replacement is atomic but **one-way**. There is no window where the binary
  is half-written, and no rollback once the rename lands. Said in the package
  doc rather than discovered.
- The SDK now has an opinion about release layout: `checksums.txt` +
  `checksums.txt.sig` + `<product>_<goos>_<goarch>.{tar.gz,zip}`, the goreleaser
  convention. A project laying out releases differently cannot use this package
  as-is.

## Breaking changes

None. `selfupdate` is a new domain in this change set: no existing symbol
changes shape, and nothing else in the SDK imports it.

## Alternatives considered

- **Leave it to consumers.** The order is four lines and getting it wrong is
  invisible — every log line looks the same whether the manifest was
  authenticated or not. That is precisely the class of thing a shared
  implementation exists for.
- **Take `SourceValue` as functional options.** `WithOwner`, `WithProduct`, …
  Four fields that are all required together is a value, not a builder; options
  would let a caller construct a Service with a product and no repository and
  discover it at the first fetch.
- **Verify the archive signature directly, skipping the manifest.** It removes
  one fetch and one indirection. It also means one signature per asset per
  platform, so a release signs N artefacts instead of one manifest covering
  them — more signing surface, and no way to detect an asset dropped from a
  release.
- **Keep the product name in the package and offer an override.** That makes one
  consumer's product the default and everyone else's an opt-out, in a domain
  with no reason to prefer any product.

## Deferred

- **A compromised host can serve an older signed release.** The signed manifest
  carries the version-independent asset name and no release tag, so answering a
  request for v2 with v1's manifest, signature and archive passes every check
  and installs v1. Verification proves provenance, not freshness. The fix is the
  tag inside the signed document — a release-FORMAT decision, not a code one,
  and one that would break every existing signed release on the day it landed.
  Stated in the package doc as a limit, not buried here.
- **Windows cannot complete the replacement.** The archive and binary naming
  handle it, and the rename cannot: Windows does not allow a running executable
  to be renamed over, and the privilege fallback is `sudo -n mv`. The known
  shapes are rename-old-aside-then-write-new, or a post-exit installer. Neither
  is written, so Windows is documented as unsupported for the replacement step
  rather than silently failing.
- **The elevated path can cross filesystems.** When the install directory is not
  writable the staging falls back to the OS temp directory, and `sudo mv` across
  filesystems becomes copy-and-remove rather than a rename — so an interruption
  can leave a partial or absent root-owned executable. Same-filesystem staging
  under the target, with the elevation applied to the rename, is the fix.

- **No rollback.** The previous binary is gone once the rename lands. Keeping it
  would mean a policy for where, for how long, and what happens when the disk is
  full — a design in its own right, and one nothing in the SDK needs yet.
- **The release layout is goreleaser's, and is not configurable.** Asset names,
  `checksums.txt` and the detached `.sig` beside it are assumed. Parameterising
  them is mechanical but has no second consumer to shape it.
- **`Getter` is one method and takes no context.** It is the port the source
  implementation had, kept verbatim so the versement stays faithful. A
  `GetContext` sibling per ADR 0039 is the obvious extension when a caller needs
  cancellation.

## References

- [ADR 0005](0005-sdk-error-codes-dotted-quad.md) — the code range this claims (`0.2.34.*`)
- [ADR 0031](0031-policy-zero-values-are-never-inert.md) — the absent key is a refusal, never an inert pass
- [ADR 0074](0074-what-a-public-alias-may-point-at.md) — `Source` and `Service` alias the engine
- ed25519 — RFC 8032, https://www.rfc-editor.org/rfc/rfc8032
- `golang.org/x/mod/semver` — https://pkg.go.dev/golang.org/x/mod/semver
