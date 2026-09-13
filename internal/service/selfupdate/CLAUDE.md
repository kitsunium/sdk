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
| `codes.go`, `errors.go` | this package's own range `0.3.66.*` and its sentinels |
| `wrap.go` | `refuse` and `classify` — the only two ways an error is built here |
| `diagnose.go` | the half of an error `Error()` never renders, for the reader entitled to it |
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

## Errors: two shapes, and which half a fact goes in

Every error here is built by one of two functions in `wrap.go`, and the choice
between them is whether this package DECIDED the failure or was TOLD about one.

- `refuse(sentinel, fields...)` — a refusal reached on our own. The sentinel is
  the cause, so `errors.Is` finds it by unwrapping as well as by (Code, Reason).
- `classify(sentinel, cause, fields...)` — a failure from outside, under the
  sentinel that says what it means here. The sentinel's Code, Reason, Public,
  Private and exit status are READ from it rather than restated at the call
  site, because twelve sites in five files reach `DownloadFailed` — eight of
  them through `classify` — and four hand-copied strings drift. `TestClassifyCarriesTheSentinelVerbatim` is what
  makes that impossible rather than unlikely.

`classify` relies on origin-wins (SDK rule 6): when the cause is already one of
ours, the sentinel named at the wrap site contributes a trail entry and nothing
else. That is why every call site can name its most specific classification
without first asking whether the cause has one — and why `decodeJSONBody`'s
`APIBodyTooLarge` survives a caller wrapping it as `ReleaseMetadataUnreadable`.

**Which half.** A public sentence names no tag, no asset, no path, no URL, no
environment variable and not one word the host said. Not squeamishness: the
public half is the one documented safe to put in a response body, and this
package's inputs are a release host and a filesystem. Those facts are not lost,
they are moved — to `Fields`, and to the cause, which stays in the chain.
`TestNoParticularReachesThePublicSentence` asserts both directions at once: the
particular is absent from `err.Error()` AND present in `diagnose(err)`, because
leaking it and losing it are both defects.

**Where the detail comes back.** `diagnose(err)` renders fields plus the first
cause this SDK did not write, and `ExplainUpgradeFailure` prints it under the
public sentence. That is the whole answer to "the operator lost `no space left
on device`": they did not, it just stopped travelling in the half that crosses
boundaries. `TestExplainUpgradeFailurePrintsTheDiagnosticHalf` pins it.

**The range.** `0.3.66.*`, allocated in `codeRangeOwners` (ADR 0035). The
domain's own `0.2.34.*` holds every refusal the CONTRACT names and none of them
is redeclared here; this range holds only what an implementation has and a
contract does not — a body that will not decode, a tar that will not open, a
temp file that cannot be created next to the running binary.

## Do NOT

- Reorder the trust chain, or check a digest against a manifest whose signature
  has not been verified.
- Reintroduce a product name, a repository or an environment variable as a
  package constant. That is `SourceValue`'s job, and the suite injects a source
  naming a product the implementation never mentioned so a reintroduced constant
  fails rather than passes.
- Add a second signature scheme. One anchor, one algorithm — a second would be a
  second thing to get right and a second thing to downgrade to.
- Call `fmt.Errorf` or `errors.New`. There are none left, the package is inside
  `//:audit_sources`, and the AST audit fails the build on the first one back.
- Put a tag, an asset name, a path, a URL, a status or a host's own words into a
  `Public` string. It is the half that may cross a wire, and there is a field
  for every one of those.
- Restate a sentinel's four strings in a `WrapParams` literal. `classify` reads
  them from the sentinel precisely so no copy exists to drift.

## Verification

```sh
bazel test --config=race //internal/service/selfupdate:selfupdate_test
go test -race ./internal/service/selfupdate/...

# the two audits this package is now subject to (ADR 0005 + ADR 0035)
bazel test //internal/kernel/errs:errs_test
bash scripts/pre-commit/check-audit-coverage.sh
```
