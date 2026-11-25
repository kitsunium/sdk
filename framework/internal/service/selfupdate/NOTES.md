# framework/internal/service/selfupdate/ — reference

Moved unchanged from `framework/internal/service/selfupdate/CLAUDE.md` (ADR 0253); each section keeps its title.

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

### The replacement is atomic, and one-way unless a probe is declared

Temp file in the target directory → `chmod 0755` → `os.Rename`. There is no
window where the binary is half-written. Without `WithProbe` there is no
rollback: once the rename lands the previous version is gone. With it, the
previous binary is hard-linked to `<binary>.prev` BEFORE the rename — so the
binary's own name is never absent, which a rename-aside would make it for a
moment — and a probe that fails renames it back (ADR 0150). A FileSystem
without the `Link` sibling keeps nothing, and a failed probe then says
`rolled_back=false`.

### Companions: one archive, one check, one set

A release archive may carry more than the product binary — ktn-linter ships a
thin hook client, `ktn-hook`, beside it — and replacing only the running binary
left the two at different versions. `WithCompanions(names...)` names the other
members. Rules, each pinned in `companions_internal_test.go`:

- **Same verified bytes.** The companions come out of the archive
  `verifyArchive` already passed; nothing is downloaded a second time, so the
  one signature and the one digest cover every file installed.
- **Installed only.** A companion is replaced at its real path (symlinks
  resolved, as the binary is) when it exists beside the binary; one that does
  not is never created — installing it is the product's installer's job.
- **One entry per real file.** `installedCompanions` keeps a set of resolved
  paths seeded with the (already resolved) `execPath`. A companion that links
  to the binary IS the binary and is skipped — it used to be renamed over it
  with the companion's bytes; a name declared twice, or two names reaching one
  file, used to put two entries in the set, and the second restore failed on
  the `.prev` the first had consumed, reporting `rolled_back=false` for a set
  that was back.
- **All or none.** Every file is staged (temp in its own directory, closed,
  `chmod 0755`) before any rename; every installed file is then hard-linked to
  `<file>.prev`; the renames run binary first; a failed rename, or a failed
  probe, renames the kept links back over every file already replaced
  (`ReplacementFailed` / `ProbeFailed`, `rolled_back=` saying whether it all
  came back). A member the archive lacks is `BinaryNotInArchive` with nothing
  replaced. Without a probe the `.prev` links are dropped once all stand; with
  one they are kept, as the single-binary probe keeps the binary's.
- **Never elevated.** A set is not moved through `sudo -n mv`: a directory the
  user cannot write, or a FileSystem without the `Link` sibling, refuses it as
  `StagingFailed` before any rename. Rolling back several privileged moves is a
  second mechanism nobody asked for.
- **No companion installed, no change.** The single-binary flow — its OS-temp
  fallback and its elevation included — runs exactly as before.

### Keys rotate, a signature names its release, the product may consent

ADR 0150 adds three things a daemon that updates itself needs, none of which
changes the historical behaviour unless asked for: several vendor keys in
order, any of which verifies (the rotation ADR 0091 gave the entitlement
anchor); a signature DOMAIN, which binds each signature to this product's
releases and requires the manifest to name its tag and an expiry — closing the
older-release replay ADR 0077 §Deferred recorded —; and `Service.WithAutomaticConsent`
(the product's own consent, an explicit environment `0` still refusing) with
`Service.WithoutElevation` (never escalate, whatever the sudo opt-in says).

### Windows is refused, before the download (ADR 0095)

Windows will not let a running executable be renamed over:
`MoveFileEx(MOVEFILE_REPLACE_EXISTING)` answers `ERROR_ACCESS_DENIED`. ADR 0077
documented the platform as unsupported for the step and nothing enforced it:
the flow downloaded and authenticated the archive, the rename failed as
`os.ErrPermission`, the privilege fallback ran `sudo -n mv` — a command Windows
does not have — and the failure advised the sudo opt-in. `canReplace` refuses a
Service built for Windows with the SDK's `UNSUPPORTED_PLATFORM` (ADR 0018),
carrying `goos` and `tag`, right after `canAuthenticate` and before a byte is
fetched — the same "refuse before spending a download the result can never be
used for" rule, and a build with no vendor key still hears about the key first.
`CheckForUpdate` works on Windows; `Upgrade` checks and then stops. It reads the
Service's `goos`, not `runtime.GOOS`, so the refusal is asserted on every host
(`TestAWindowsTargetRefusesTheReplacementBeforeDownloadingAnything`,
`TestUpgradeOnAWindowsTargetChecksAndThenStops`).

### The suite builds every Service for one platform

`TestMain` pins `runtimeGOOS` to `fixtureGOOS` (`linux`) once, before any test
runs. The pipeline's fixtures are built for one platform — a tar.gz, a manifest
naming that platform's asset — and left to the host they changed meaning on
Windows (a zip expected, the tar.gz refused as `ARCHIVE_UNREADABLE`, and now the
platform refusal ahead of every gate). One assignment before `m.Run` races
nothing; the warning on `Service` is about mutating it under `t.Parallel`. The
Windows shapes (zip, `.exe`, the refusal) are asserted by tables that build a
Service per platform explicitly.

## Where ADR 0158 §2 is not followed, and why

Signatures go through `pkg/v1/crypto/sign` (`keys.go`), the archive digest through
`pkg/v1/crypto/hash` (`checksum.go`), the statement's expiry through an injected
`pkg/v1/clock` clock and the Windows refusal is `pkg/v1/proc`'s. HTTP is the
exception: `newReleaseHTTPClient` (`transport.go`) stays a `net/http` client over
a clone of `http.DefaultTransport` rather than `pkg/v1/net/client`, because the
guarded transport sets no `Proxy` and so ignores `HTTP(S)_PROXY` and `NO_PROXY` —
measured, it dialled the host directly where the default transport went through
the proxy — and an update that cannot leave a network through its proxy never
reaches a machine behind one. Adopting it waits for a proxy option in the net
domain's `ClientConfig`; the guarded policy would then also have to re-state
this client's https-only, bounded hops.

A clone, never the default itself (issue #304): the clone keeps the default's
settings, its proxy first, and owns its connection pool. The default's pool is
the process's, and anything may empty it — `httptest.Server.Close` does on every
close, `CloseIdleConnections` on any client with a nil `Transport` too. net/http
puts a connection back in the idle pool before it hands a body-less response to
the request waiting on it, so an outside close in that window broke the request
in flight with `HTTP/1.x transport connection broken: http: CloseIdleConnections
called`, and a downgrade the redirect policy would have refused surfaced as a
transport fault instead of `InsecureRedirect`.
`TestNewReleaseHTTPClient_ownsItsConnections` holds it: a structural row, and a
row racing the default pool's close against eight callers (failed 9 runs in 20
before the clone).
