# ADR 0100 — a program reads what it was built from, and what it is doing, and asks git only about a working tree

- **Status**: Accepted
- **Date**: 2026-09-25
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0076](0076-what-a-branch-changed-is-a-value-that-can-say-it-does-not-know.md) (the git engine gains a query the port does not model)
- **Related**: [ADR 0016](0016-sdk-process-supervision-domain.md) (the `proc` domain), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (whose type a value is), [ADR 0018](0018-sdk-cross-platform-portability.md) (every GOOS builds)

## Context

A framework built on this SDK shows, in its developer console, what the running
product is: the module and version it was built from, the versions of the
framework and of the SDK inside it, which of those came from a local directory
and what commit that directory is on, and the process's live state —
goroutines, heap, collections, CPU. It wrote all of that itself, about 350
lines, because nothing here said any of it:

- **The git domain answers one question.** ADR 0076 made it deliberately thin:
  what a branch CHANGED, and no model of commits, refs or history. A caller
  wanting "which commit is this working tree on, when was it made, is it
  dirty" ran `git rev-parse HEAD`, `git log -1 --format=%cI` and
  `git status --porcelain` by hand — with none of the hardening
  `internal/service/vcs/git` applies to every invocation. Measured against a
  repository whose `.git/config` sets `core.fsmonitor` to a script: a plain
  `git status --porcelain --untracked-files=no` executed it **twice**. And
  `git log` honours `log.showSignature`, which hands a `gpg.program` the same
  configuration can name a signature to "verify".
- **`runtime/debug.BuildInfo` is read three ways and each copy is partial.** The
  framework's reading kept a release version, took a pseudo-version for "no
  version" and — for a dependency — recovered its commit prefix with a
  hand-written pattern; it marked a directory replacement local and left a
  workspace module neither versioned nor local. What the toolchain actually
  records was measured rather than assumed, on go1.27.1: in workspace mode a
  directory replacement written `../lib` in `go.mod` is recorded as `./lib` —
  relative to the WORKSPACE root — with version `(devel)`; a workspace module is
  recorded as `(devel)` with no replacement and no directory at all; and since
  Go 1.24 a main module built from a modified checkout carries `+dirty` in its
  version.
- **The proc domain reads children only.** `ExitValue` carries a child's CPU
  times and peak RSS from `wait4`; nothing describes the process itself. The
  framework read `runtime/metrics` (goroutines, heap, heap goal, mapped memory,
  allocations, collections, the GC-pause and scheduling-latency histograms),
  `debug.ReadGCStats` for the last collection, and `getrusage(RUSAGE_SELF)` —
  or, where that does not exist, the runtime's total CPU minus its idle time.

## Decision

### D1 — `git.Head`: HEAD, its time, and whether tracked files differ

`internal/service/vcs/git.Head(ctx, dir) (HeadValue, error)`, published as
`pkg/v1/git.Head` returning `HeadState`. Three invocations, all through the
hardened runner:

1. `rev-parse --verify HEAD^{commit}` — the revision. On failure a probe
   (`rev-parse --git-dir`) tells "no repository here" (`RepositoryUnresolved`)
   from "a repository whose HEAD names no commit yet" (`CommandFailed`), the way
   `ShowFile` classifies — never by reading git's localised stderr.
2. `cat-file commit <rev>` — the time, parsed from the committer line of the
   raw object: the last two fields after the email's closing `>`, in the offset
   they were recorded with, which is what `%cI` prints. Not `git log`: it
   honours `log.showSignature`, and `cat-file` verifies nothing.
3. `--no-optional-locks status --porcelain --untracked-files=no` — modified
   when it prints anything. `--no-optional-locks` keeps a read-only question
   from taking the index lock a concurrent `git commit` needs. Untracked files
   do not count: they are not part of what the commit describes. That is
   narrower than Go's own `vcs.modified`, which counts them, and both docs say
   so.

A status that fails is an error, never "clean" — clean is a claim git refused
to make. Nothing is cached: modified is the one fact here that changes without
a commit, and a caller asking often keeps its own answer.

`HeadValue` lives in the SERVICE (ADR 0074). The port, `core/vcs.ChangedSet`,
still models no commit, and a second implementation of it would have no reason
to produce this value; ADR 0076's thinness is kept where it was argued — in the
contract — and the engine gains a query.

### D2 — the running binary's build, in the proc domain

`internal/service/proc/self.ReadBuild()` / `ParseBuild(*debug.BuildInfo)`,
published as `pkg/v1/process.Build()` / `ParseBuild()` with `BuildInfo` and
`Module`. A recorded version conflates three things and `ModuleValue` keeps
them apart:

- a RELEASE — `Version`, and only when it is one;
- a COMMIT — `Revision` and `Time`: the main module's `vcs.revision` /
  `vcs.time` stamp, or what a pseudo-version names;
- a DIRECTORY — `Local` and `Dir`: a directory replacement (with the path as
  recorded), a workspace module (no path — the toolchain records none), or a
  main module built in its own tree.

A dependency is described by the code actually built for it, so a replacement
is followed: a directory replacement is local with no version, a module
replacement contributes its version and its path (`Replacement`). The main
module's stamp beats its pseudo-version — a full object name against a
twelve-character prefix, the exact time against a second-granular copy — and
`+dirty` becomes `Modified` instead of staying in the version string.
Pseudo-versions are recognised and split by `golang.org/x/mod/module`, the
toolchain's own grammar, which the service module already required for
`semver`: no new module, and none of the three forms or their build metadata
left to a hand-written pattern.

### D3 — the running process's state, in the same package

`self.ReadStats()`, published as `pkg/v1/process.Self()` with `Stats` and
`Distribution`: one `metrics.Read` over nine runtime metrics, one
`debug.ReadGCStats` for the end of the last collection (runtime/metrics has no
such figure, and ReadGCStats copies it without stopping the world), and the CPU
time — `getrusage(RUSAGE_SELF)` where it exists, the runtime's estimate
elsewhere, with `CPUEstimated` saying which. The estimate counts only threads
running Go code, so it misses cgo and time blocked in the kernel, and the
runtime refreshes it only at a garbage collection's stop-the-world — measured:
unchanged across 300 ms of spinning, zero before the first collection, caught
up by the next `runtime.GC`. A field that said nothing about either would be
read as the kernel's number; the framework's copy fell back to it unflagged.

Everything is cumulative since the process started, the two histograms
included: a window is two snapshots subtracted, and a snapshot that reset the
runtime's counters would break every other reader. `Distribution.Quantile(q)`
is the UPPER bound of the bucket where the cumulative count reaches q — the
honest reading of a bucketed histogram — and `q = 0` lands on the first bucket
that holds an observation rather than on the first bucket. `Started` is this
package's initialisation, documented as such: `/proc/self/stat` would be exact
on Linux and nothing elsewhere, and a field that meant different things per
platform would be worse than a stated approximation.

Nothing in D2 or D3 returns an error. No build information, a metric the
toolchain does not export, a platform without `getrusage` — none is a fault a
caller can act on, and each is a `false`, a zero, or a flag.

### D4 — placement

`self` is a new service package under `proc` because the subject is the
process: the rest of the domain acts on children, this reads the process it
runs in. It is a facade addition to `pkg/v1/process`, whose rule — aliases and
one-line delegations, no new named types — is unchanged. The values live in
the service by ADR 0074: no port of the proc domain speaks them.

## Consequences

- The framework can delete `gitAt` (its per-directory cache stays, as its own
  policy), `buildOf` / `moduleOf` / `pseudo` and `kitVersion`'s reading of the
  build, and `sampleProcess` / `percentile` / `cpuSeconds`. What
  it keeps is its display: which two dependencies it names, how it spells
  "local", its rounding to milliseconds.
- `Head` costs three subprocesses — a few milliseconds — which is why it caches
  nothing itself and says so. `Self` costs one runtime-metrics read and two
  syscalls.
- A consumer that wants to know what a DIRECTORY holds now combines the two:
  `process.Build` says a module came from `Dir`, `git.Head(ctx, Dir)` says what
  that tree is at. The package docs point each at the other.

## Breaking changes

None. Every symbol is new; `core/vcs`, `core/proc` and every existing signature
are unchanged.

## Alternatives considered

- **`HeadValue` in `core/vcs`.** The port would then model a commit, which ADR
  0076 §Alternatives argued against and nothing here needs: the question is
  answered by one engine, and ADR 0074 puts such a value with the engine.
- **`git log -1 --format=%H%x00%cI`, one call instead of two.** Rejected for
  `log.showSignature`: the planted `gpg.program` runs on a query the caller
  believes is read-only. Pinning `-c log.showSignature=false` would close that
  one key and leave the next pretty-format feature to be audited; `cat-file`
  has none.
- **A `buildinfo` kernel package.** It would be stdlib-only and domain-free,
  and could not use `x/mod`: the three pseudo-version forms and their build
  metadata would be a second grammar to keep in step with the toolchain's. And
  "what the running process is made of" is the process's subject.
- **A `buildinfo` domain with a core package.** There is no port and no error;
  a core package holding two value types and nothing else is the stub rule 5
  forbids.
- **The p99 alone, as the framework exposed it.** A `Distribution` with
  `Quantile` costs the same read and lets a caller ask for p50 or p999 without
  a new field per percentile.

## Deferred

- **Clean filters.** A clean filter planted in `.git/config` and bound by a
  `.gitattributes` runs whenever git must re-hash a tracked file whose size did
  not change. Measured against the hardened runner: once during `Head`'s
  `status`, seven times during `Resolve`'s `diff`. `hardenedGitConfig` cannot
  neutralise it with a fixed `-c`, because the key is `filter.<driver>.clean`
  and the driver name is the repository's to choose; closing it means reading
  the configured driver names first and emptying each, in the shared runner,
  for every invocation. It is `Resolve`'s exposure as much as `Head`'s, so it is
  recorded here and not half-fixed for one caller.
- **The kernel's CPU count on Windows** (`GetProcessTimes`, bound from
  `kernel32` as `lock` binds `LockFileEx`). Until then `CPUEstimated` is true
  there.
- **The kernel's process start time.** `/proc/self/stat`, `sysctl
  KERN_PROC_PID`, `GetProcessTimes` — three mechanisms for one field.

## Verification

- `internal/service/vcs/git/head_external_test.go` — a real repository with a
  fixed committer date in `+02:00`: revision, time and offset, from a
  subdirectory; unstaged and staged changes modify, an untracked file does not;
  no repository, a missing directory and an unborn branch refuse with the right
  code and no value.
- `internal/service/vcs/git/head_internal_test.go` — the committer-line parser
  over a signed commit with a bracket in the name and a committer line in the
  message; `TestHeadRefusesRepositoryControlledExecution` plants
  `core.fsmonitor` and asserts zero executions and a dirty tree still read as
  dirty.
- `internal/service/proc/self/build_external_test.go` — every recorded shape
  above, including the stamp beating the pseudo-version and `+dirty` beating a
  `vcs.modified=false`; `ReadBuild` against `debug.ReadBuildInfo`.
- `internal/service/proc/self/stats_external_test.go` — identity, a live heap,
  a collection after `runtime.GC`, CPU time after 50 ms of spinning, the
  estimate flag per GOOS, and no cumulative figure going backwards;
  `stats_internal_test.go` — `Quantile` at the open ends, at `q = 0`, on NaN.

## References

- [ADR 0076](0076-what-a-branch-changed-is-a-value-that-can-say-it-does-not-know.md) — the git domain's scope, amended here at the engine only.
- [ADR 0074](0074-what-a-public-alias-may-point-at.md) — a value one engine produces lives with the engine.
- [`runtime/debug.BuildInfo`](https://pkg.go.dev/runtime/debug#BuildInfo), [`golang.org/x/mod/module`](https://pkg.go.dev/golang.org/x/mod/module) — `IsPseudoVersion`, `PseudoVersionRev`, `PseudoVersionTime`.
- [`runtime/metrics`](https://pkg.go.dev/runtime/metrics) — `/sched/pauses/total/gc:seconds`, `/sched/latencies:seconds`, `/cpu/classes/*`.
- [git-config: `log.showSignature`, `gpg.program`, `core.fsmonitor`, `filter.<driver>.clean`](https://git-scm.com/docs/git-config).
