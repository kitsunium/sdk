# internal/service/selfupdate/

## Purpose

Implements the `core/selfupdate` contract: fetch a release, authenticate it, and
replace the running binary (ADR 0077). Internal service behind `pkg/v1/selfupdate`.

Its only non-stdlib dependency is `golang.org/x/mod/semver`, which is itself
stdlib-pure — three functions (`Compare`, `IsValid`, `Prerelease`), no transitive
module.

## Contents

| File | Role |
|---|---|
| `updater.go` | `Service`, the update flow, the atomic replacement |
| `source.go` | `SourceValue` — the whole of what the original hard-coded |
| `signature.go` | authenticity: detached ed25519 over the manifest, `WithVendorKey` |
| `checksum.go` | integrity: SHA-256 against the ALREADY-AUTHENTICATED manifest |
| `transport.go` | bounded, https-only redirects and the response read cap |
| `consent.go` | whether an unrequested upgrade may proceed, and the advice when it may not |
| `elevation.go` | the second, separate opt-in for a non-writable install directory |
| `candidate.go` | the release-candidate channel |
| `interfaces.go`, `osfilesystem.go`, `stdiocopier.go` | the port implementations |

## Why-this-shape

### The order, again

`verifyArchive` runs the signature check BEFORE the digest comparison, and the
suite asserts the order rather than trusting a comment. Verifying the digest
against a manifest nothing vouched for is decoration: an attacker substituting
the archive substitutes the manifest beside it.

### SourceValue is the versement's whole point

The implementation this came from hard-coded `repoOwner`, `stableRepo`,
`devRepo`, the asset name pattern, the inner binary name, and two environment
variables. Every one is a property of one product's distribution rather than of
the update mechanism, and together they made a good implementation serve exactly
one binary.

`SourceValue.envPrefix` uppercases the product and folds punctuation to
underscore, which is not a free choice: the original pinned
`KTN_LINTER_AUTO_UPGRADE` and `KTN_LINTER_ALLOW_SUDO` against renaming, because
"a renamed variable strands every environment already setting it". `ktn-linter`
folds to `KTN_LINTER`, so those two survive the parameterisation exactly.
`TestEnvNamesAreDerivedNotInvented` pins it.

`StableRepo` and `DevRepo` can differ because a project whose source repository
goes private still has to serve already-installed binaries an update path: a
public mirror for stable releases, candidates left behind credentials the public
binary does not carry. An empty `DevRepo` means one repository serves both.

### Two opt-ins, never one

Authorising an unattended upgrade must not grant privilege escalation.
`AutoUpgradeEnv` and `SudoOptInEnv` derive separately and a test pins that they
never collide.

### The replacement is atomic, and one-way

Temp file in the target directory → `chmod 0755` → `os.Rename`. There is no
window where the binary is half-written. There is also no rollback: once the
rename lands the previous version is gone.

## Known debt: `fmt.Errorf` in production code

**75 call sites in this package build errors with `fmt.Errorf` rather than
`errs.Wrap`.** SDK-wide rule 2 bans it, and this package is the largest violation
in the tree — the next worst is `internal/service/cache` with 3.

It is recorded rather than silently carried. The sentinels themselves ARE typed
(`core/selfupdate`, range `0.2.34.*`) and every site wraps one with `%w`, so
`errors.Is` and `errs.HasCode` both work through them; what is missing is the
`Public`/`Private` split and the wrap trail on the CONTEXT each site adds.

It came in with the versement: the source implementation predates this rule and
converting 75 sites in the same change that moves them would have made the diff
unreviewable against its origin. The conversion is mechanical, it is the next
change this package should receive, and it is tracked in ADR 0077 §Deferred.

Note also that this package is deliberately absent from `//:audit_sources` today:
adding it would fail the AST audit on exactly these 75 sites, which is the right
outcome once they are converted and the wrong one as a way of blocking a merge.

## Do NOT

- Reorder the trust chain, or check a digest against a manifest whose signature
  has not been verified.
- Reintroduce a product name, a repository or an environment variable as a
  package constant. That is `SourceValue`'s job, and the suite injects a source
  naming a product the implementation never mentioned so a reintroduced constant
  fails rather than passes.
- Add a second signature scheme. One anchor, one algorithm — a second would be a
  second thing to get right and a second thing to downgrade to.

## Verification

```sh
bazel test --config=race //internal/service/selfupdate:selfupdate_test
go test -race ./internal/service/selfupdate/...
```
