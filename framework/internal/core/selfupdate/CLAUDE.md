# framework/internal/core/selfupdate/

## Purpose

The contract for a binary that replaces itself (ADR 0077): the three ports it
needs from its environment, the values it reports, and the eighteen sentinels it
can refuse with. Interfaces and immutable values only.

Code range `0.2.34.*` (`0x00_02_22_*`), owned solely by this package. It is a
layer-2 value although the package is the framework's: it moved here from the
SDK library's `internal/core/selfupdate` with ADR 0158, and a code keeps its
value when it moves (ADR 0160). Imports: the standard library and
`internal/kernel/errs`, nothing else (framework/CLAUDE.md rule 1).

## Contents

| File | Role |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167): why the ORDER of the trust chain is the contract |
| `decl_gen.go` | written by kit gen from the design (ADR 0170): the declarations of `Getter`, `FileSystem`, `Copier`, `UpdateValue` and `CandidateValue` — each struct with every field, unexported ones included. Their methods, constructors and helpers stay hand-written, in the files this document names |
| `codes_gen.go` | the range and its eighteen sentinels — written by kit gen from `design/framework/selfupdate.yaml` (ADR 0164) |

## Why-this-shape

- **The order is the security property, and `codes_gen.go` states it.** A detached
  signature over the checksum manifest, then the archive's digest against that
  now-authenticated manifest, then the disk. A digest checked against an
  unauthenticated manifest proves nothing — whoever can substitute the archive
  can substitute the manifest beside it — so `CodeChecksumMismatch` is only
  reachable once `CodeSignatureInvalid` was not.
- **`NoVendorKey` is a refusal, not a degradation.** Without a key nothing could
  have authenticated the release, and there is no weaker check to fall back to.
  The alternative — verify when a key is present, skip when it is absent — fails
  OPEN and makes the guarantee depend on a build flag nobody inspects.
- **Eighteen codes, not one.** A caller has to tell apart what a retry fixes
  (`DownloadFailed`), what an opt-in fixes (`ElevationNotAuthorised`), and what
  nothing fixes and no manual workaround is safe for (`SignatureMissing` /
  `SignatureInvalid`). Collapsing them would make the advice generic exactly
  where it must not be.
- **`EvalSymlinks` is in the `FileSystem` port, not decoration.** An executable
  reached through a symlink must be replaced at its REAL path, or the update
  writes over the link and the next run still executes the old binary.
- **No registry.** A registry's key would name a release host, and the whole
  trust chain is anchored to a key pinned at build time for exactly one host.

## Do NOT

- Add a "skip verification" path, however it is spelled.
- Put the release host, the product name or an environment-variable name here.
  Those are one product's distribution, and they live in the service's
  `SourceValue` precisely so this contract stays about the mechanism.
- Import anything under `internal/` but `internal/kernel/errs` (ADR 0147 §2).

## Verification

```sh
bazel test --config=race //framework/internal/service/selfupdate:selfupdate_test
(cd framework && GOWORK=off go test -race ./internal/core/selfupdate/... ./internal/service/selfupdate/...)
```
