# Notes — <!-- updated: 2026-10-04T00:32:31Z -->

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles. `CLAUDE.md` keeps what to obey; this file holds the rest.

## Freshness probe

Beside the rules, sdkguard warns when the consumer's `go.mod` pins an SDK older
than the newest release — the SDK module, `github.com/kitsunium/sdk` (ADR 0162):

```
sdkguard: warning: the SDK is 15 patch releases behind — go.mod requires v0.18.0, latest is v0.18.15.
  A patch is cut whenever an internal package the public packages depend on
  changes (ADR 0007), so releases carry fixes that never alter the public API.
  Update:  go get github.com/kitsunium/sdk@latest && go mod tidy
  Silence: -version-check=off
```

The update line is `go get …@latest && go mod tidy` for the root
(`updateCommand`): since ADR 0171 the root holds the framework alone and
`pkg/v1` is a module per family, so raising the root alone would leave every
`pkg/v1` import provided by nothing, and the tidy resolves each to its family
module. The probe still reads the root's requirement only — a go.mod that
requires families alone is not told one is behind (ADR 0171 §Deferred).

This exists because of how the SDK versions. Per ADR 0007 a patch is cut
whenever an `internal/*` package that the public packages depend on changes — so
patches carry fixes that never touch the public API.

A `go.mod` that still requires `github.com/kitsunium/sdk/pkg` — the public
module before ADR 0162 — is behind by a module rather than a version. Once the
SDK module has a release on the proxy, the warning is the migration: the
release, and the one `go get` that requires it and drops `…/pkg`,
`…/framework` and the three `…/internal/*` modules together, without which the
build fails on an ambiguous import (`migrateCommand` in `version.go`). The drop
is one argument, `…/internal/kernel@none` (`retiredRoot`, `dropMerged`): every
version of the other four up to v0.17.0 requires it, so `go get` removes them
with it — measured over the 240 the proxy holds, and against a file proxy
(ADR 0162). A vendor or connector module is named at `@latest`, since a release
tags one only when it changed. Until that release exists the old probe still
answers for `…/pkg`. A `replace` of `…/pkg` silences it like any local
checkout.

**The SDK module's releases start at v0.18.0** (`firstSDKModuleRelease`). The
proxy also lists `github.com/kitsunium/sdk` at `v0.0.0`, a tag of an earlier
history whose zip holds a LICENSE and no package. Read as "the SDK module has a
release", it told every `…/pkg` consumer to migrate to that empty module, and a
build on a pseudo-version of `main` to "update" to it. No version below the
floor is a candidate, in either notice.

**A half-migrated go.mod is told from the go.mod alone.** One that requires the
SDK module and still a module it merged — what the command without the three
internal modules leaves, since `go mod tidy` lists them as `// indirect`, or
an upgrade of `…/pkg` and `…/framework` to v0.18.0 — finds each package of that
module twice and fails on an ambiguous import. The notice names the merged
module and the removal (`halfMigrationNotice`), with no network call; a merged
module the build `replace`s does not count, and neither does `…/pkg` or
`…/framework` at its tombstone (`isTombstone`): v0.18.0 of each, cut once with
the SDK module's first release, holds a go.mod and no package, so nothing of
it is found twice.

A consumer reading a changelog of exported symbols sees nothing and concludes
there is nothing to take. That is the quiet cousin of the defects the rules
catch: nothing fails, and the fix simply never arrives.

**In the default mode it is a warning, and the exit code does not move.** Being behind is not a
violation — it is a fact the maintainer may already know and may have decided to
live with. `-version-check=error` promotes it for teams that want freshness
gated; `-version-check=off` skips the probe entirely, making no network call.

**Every failure path degrades to silence**, because a freshness nudge that
breaks a build has failed at being a nudge:

| Situation | Behaviour |
|---|---|
| No `go.mod`, or no SDK requirement (of `github.com/kitsunium/sdk`, or of `…/pkg` before it) | silent |
| The proxy lists the SDK module at `v0.0.0` only | no release yet: a `…/pkg` go.mod is measured against `…/pkg`, one on the SDK module is silent |
| A `replace` directive on the SDK | silent — that is a local checkout, not a stale pin |
| `GOPROXY=off`, or a proxy list with no usable URL | silent, no network call |
| Primary proxy down, backup listed | falls through to the next entry — the list is walked in order |
| A workspace root with no `go.mod` | resolved through `go.work`; the OLDEST module's requirement is reported, so a stale module cannot hide behind an up-to-date sibling |
| Several roots given | each module is probed once; results do not depend on argument order |
| Proxy unreachable, timeout (3s), non-200, malformed body | silent |
| Consumer ahead of the proxy (unreleased local tag) | silent |

`GOPROXY` is read the way the go command reads it — `,`/`|` fallback lists,
`off`, `direct`. Only strict `vX.Y.Z` tags are candidates: recommending a
prerelease or a pseudo-version as "the latest" would be wrong advice. Ordering
is numeric, not lexical, so `v0.1.9` correctly precedes `v0.1.24` — a string
comparison would invert that and tell an up-to-date consumer to downgrade.

`make guard` passes `-version-check=off`: the SDK is not a consumer of itself,
and `make lint` must not depend on network egress.

## Contents

| File | Role |
|---|---|
| `main.go` | CLI entry, flags (`-tests`, `-rules`, `-level`, `-version-check`, `-list`), exit codes |
| `analyze.go` | walking, parsing, import-alias resolution, shadowed names (`shadowedNames`, which skips a type's members — `memberFields`), suppression directives |
| `file_ctx.go` | `fileCtx` — what every rule reads about one parsed file: its path, the local name of each import, the lines carrying an exemption |
| `rules.go` | the rule table and the six checks |
| `pipeline_call.go` | the `log/slog` entry points SDK001 watches, in reporting order |
| `version.go` | the freshness probe: go.mod reading for the SDK module and, before it, `…/pkg` (ADR 0162), GOPROXY resolution, semver ordering, the floor of the SDK module's releases, the tombstones that are no half-migration, the migration and half-migration notices and their one removal |
| `probe.go` | the module-proxy client behind the probe, its proxy and HTTP client fields so a test points it at `httptest` |
| `errors.go` | `errProxyDisabled` — sdkguard cannot use `pkg/v1/errs`, since it must run without pulling the library it audits |
| `main_test.go` | every test: one fire + one silence case per rule, alias, suppression, level and ordering, and the probe — semver ordering, go.mod shapes, GOPROXY resolution, the degrade-to-silence paths, the floor of the SDK module's releases, the tombstones and the two migration notices, served by `httptest` so no test touches the network |

Exit codes: `0` clean, `1` findings, `2` the tool itself failed — so CI can tell
"rules broken" from "tool broke".
