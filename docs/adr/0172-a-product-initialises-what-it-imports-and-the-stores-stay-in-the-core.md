# ADR 0172 — a product initialises what it imports, and the stores stay in the core

- **Status**: Accepted
- **Date**: 2026-10-12
- **Deciders**: SDK maintainers (issue #273, the owner's request of 2026-10-12 to measure first and take the smallest change)
- **Amends**: [ADR 0100](0100-a-program-reads-what-it-was-built-from-and-asks-git-only-about-a-working-tree.md) (the build and the runtime state of a process are two packages, not one)
- **Related**: [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) (the framework above `pkg`), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (the v0 licence), issue #272 (the server and the Studio left kit's graph), issue #273 (the proposal), issue #276 (the status line's start-up)

## Context

Go runs the initialisation of every package a binary imports, whether the
program reaches it or not. #272 took the opt-in subsystems — the HTTP engine,
the frontends' file server, the Studio's event stream and profiler — out of
`framework/internal/kit`'s import graph behind hooks (`plug`), implemented by
`serverkit`/`studiokit` and plugged in by `framework/kit/server` and
`framework/kit/studio` as they are imported. #273 proposed the same for what a
declaration needs — stores (`docstore`, `sql`, `database/sql`), topics and
queued commands (`queue`), workflows (`statemachine`), mailers (`mail`,
`mail/spool`), jobs (`scheduler`), kit's catalogues (`i18n`) — as a breaking
change: one registration package per subsystem, a start refusal naming the
missing import. #276 attributed most of the status line's +1.2 ms per redraw
since the kit migration to package initialisation (3.14 against 1.81
Mcycles) and named #273 as the lever.

The proposal was measured before anything was moved, on
`framework/kit/testdata/cliprobe` — one service, one default fail-safe CLI
command, the smallest product — built from the branch before this change, on
an AMD EPYC 7351P
(8 vCPU, Linux 6.12, go1.27.2). Initialisation is `GODEBUG=inittrace=1`, the
minimum per package over 30 runs, summed; wall and CPU are the medians of ten
alternating rounds of 300 execs; the dependency sets are `go list -deps`
differences over `framework/internal/kit`'s own imports.

| What | Packages | Init (ms) |
|---|---|---|
| The whole probe (369 deps, 19.8 MB) | 188 with an init | 0.72 |
| #273's set — stores, sql, queue, workflows, mail, jobs, i18n | 27–31 | 0.083 |
| Every SDK package kit could drop (#273's set, plus health, resilience, authz, metrics, trace, password, vfs, validation, id, lock) | 56 | 0.152 |
| `net/http` and what it brings | 189 | 0.294 |
| `pkg/v1/proc/process` alone: `runtime/metrics`, the spawn engine, `os/user` | 8 | 0.046 |

Three further facts:

1. **The time a status line loses is mostly not package initialisation.** An
   empty Go program costs 2.1 ms here, the probe 4.9–5.1 ms; of the 2.8 ms
   between them, initialisation is 0.72 ms. The rest is the binary's own
   weight: 11.6 MB of the 19.8 MB is code reachable only from `App.Main`, and
   linking it without running it costs ~75 page faults and ~0.2 ms by
   itself.
2. **The stores cannot leave through a hook as kit is written.** A store's
   engine is generic — `storeEngine[T]`, `docEngine[T]`, `sqlEngine[T]`,
   `historied[T]` — instantiated by kit for each entity type. A hook is a
   value, and a value cannot be a generic function, so kit cannot ask
   `framework/kit/store` for a `docEngine[Order]` without importing it. The
   engine would first have to become one non-generic core under a typed
   wrapper, the shape `sealedEngine` already has for sealed records; the
   history, privacy, revisions, sealing and transaction code that reads
   `docstore`'s and `sql`'s types — 8 800 lines in 29 files that import them
   — would move with it. That is the redesign the proposal's own option 2
   described, and option 1 needs the same surgery: only who imports the
   registration package differs.
3. **Moving the smaller subsystems buys the status line nothing.** Its daemon
   role declares jobs (`Service.Every`), and both roles are one binary, so it
   would import the jobs package anyway. `Mailer.Send` takes the SDK's
   `mail.Message`, so the mail package stays in the graph of every product
   whatever the hook. Mail's spool, workflows and jobs together are 14
   packages and 0.026 ms.

What the measurements did find is a subsystem whose reader IS opt-in: the
process sample. kit imported `pkg/v1/proc/process` for two things —
`ParseBuild`, which every app needs, and `Self`, which only the runtime graph
a serving app answers carries. The package also holds the spawn engine, and
`Self` is `runtime/metrics`, whose initialisation alone is 0.031 ms — more
than mail, workflows and jobs together.

## Decision

### 1. The rule #273 asked for is the rule, applied where its reader is opt-in

A product initialises what it declares or imports. A subsystem leaves kit's
import graph through `plug` when what reads it is itself opt-in — imported by
a product that uses it — and the move needs no generic engine to cross a hook.
`framework/kit`'s `TestTheFacadeImportsNoOptInSubsystem` holds the graph to
it: `go list -deps framework/kit` contains none of the packages an opt-in
subsystem brings.

### 2. The process sampler is `framework/kit/server`'s

`plug.SampleProcess` is a hook set by `serverkit.Enable`, beside the HTTP
engine; `serverkit/sampler.go` holds the sampler that was kit's `process.go`.
kit's runtime graph and the dev route `GET /_kit/api/process` read it through
the hook, and carry no process without it. A product that does not import
`framework/kit/server` — every CLI and daemon — initialises neither
`runtime/metrics` nor the SDK's process package.

### 3. A build is read by a package of its own

`internal/service/proc/self` held both what the binary was built from
(`build.go`) and what the process is doing (`stats.go`, which imports
`runtime/metrics`). The build moves to `internal/service/proc/buildinfo`,
published alone as `pkg/v1/proc/buildinfo` (`BuildInfo`, `Module`, `Build`,
`ParseBuild`). `pkg/v1/proc/process` keeps re-exporting the same aliases and
functions, now forwarding there, so no consumer of it changes. kit reads its
build through `pkg/v1/proc/buildinfo`.

### 4. The declared subsystems stay in the core

Stores, `sql`, queues and topics, workflows, mailers, jobs and kit's
catalogues stay in `framework/internal/kit`'s graph, and no registration
package is required of a product. The breaking option of #273 is not taken:
its ceiling is 0.083 ms of initialisation on this machine, it cannot be
reached for the stores without first redesigning their generic engines, and
the product it was meant for would import the jobs package regardless.

What reopens it, by name: a store engine that is non-generic under a typed
wrapper (then a hook can carry it, and the stores' share of the binary —
`internal/service/data/docstore` alone is 0.55 MB of symbols in the probe —
becomes the measure, not their 0.024 ms of initialisation); or a product
whose binary declares none of a subsystem and measures the difference.

## Consequences / Semantics

- `go list -deps ./framework/kit/testdata/cliprobe`: 369 → 364 (−7, +2: the
  two `buildinfo` packages). Packages with an init: 188 → 184. The binary
  loses 92 KB. The wall time does not resolve it: built `-trimpath` and
  measured beside a byte-for-byte control (`kit startup`,
  kitsunium/platform#80), the delta stays inside what separates identical
  bytes exec'd from two paths — 0.19 ms on this machine
  (`framework/kit/BENCH.md` §Controls). A first measurement without
  `-trimpath` read 5.10 → 4.99 ms and is withdrawn: the build directory is
  part of such a binary.
- A serving app's runtime graph is unchanged: `framework/kit/server` is
  already imported by every product that serves the graph.
- `serverkit` now imports `pkg/v1/proc/process`; a product that serves HTTP
  initialises it as before.
- What remains of #276 is not in the import graph: the generated error
  sentinels (`errs.Define`, ~380 calls in the probe, one allocation and a
  validation each at every start; a straight-line constructor the compiler
  initialises statically removed 34 packages' init entirely in an experiment,
  0.1–0.2 ms) and the binary's size. Both are recorded in #276, not decided
  here.

## Breaking changes

- `App.Graph().Runtime.Process` is nil, and `GET /_kit/api/process` answers
  404, in a program that does not import `framework/kit/server`. Such a
  program serves no route, so only an in-process caller of `App.Graph` on a
  running daemon or CLI sees it. The shape of `model.Runtime` is unchanged;
  its behaviour changes under ADR 0040's v0 licence, which this module still
  holds — named here because a v1 could not.
- `process.BuildInfo` and `process.Module` alias the same declarations,
  now defined in `internal/service/proc/buildinfo` rather than
  `internal/service/proc/self`: a consumer naming them is unaffected, while a
  `%T` or a reflected type name reads `buildinfo.BuildValue` where it read
  `self.BuildValue`.
- Nothing else. `pkg/v1/proc/buildinfo` is new.

## Alternatives considered

- **#273 option 1, the registration packages, now.** Refused for this
  change: §Context 2 and 3. Its measured ceiling (0.083 ms) is below what
  §2–3 recover without breaking a product.
- **#273 option 2, every engine behind a hook, non-breaking.** The same
  generic-engine obstacle, and the facade would still import the registration
  packages to stay non-breaking — no gain.
- **Plugging the sampler from `framework/kit/studio`.** The Studio is mounted
  only in a serving app, and the runtime graph is served without it; the
  server is where every reader is.
- **Reading `debug.BuildInfo` in kit by hand.** It is a mechanism the SDK
  already has (`ParseBuild`, with the pseudo-version grammar of
  `kernel/semver`); the framework's rule is to take the mechanism from
  `pkg/v1`, so the package was split instead.

## Deferred

- The generated sentinels' initialisation (#276): a straight-line,
  statically-initialisable constructor needs the design's `options:` — Go
  expressions today (`errs.WithExitCode(exitConfig)`) — to become fields kit
  can write as constants, which is a change of kit's design format.
- `internal/service/app/id`'s registry (0.015–0.031 ms: the snowflake node
  derived at init) and `docstore`'s table-name pattern compiled at init
  (0.018 ms) — #276.
- The stores' engine as one non-generic core (§4), the precondition of every
  other move of #273.

## References

- `framework/kit/initgraph_external_test.go`, `framework/internal/kit/plug/plug.go`,
  `framework/internal/kit/serverkit/sampler.go`
- `internal/service/proc/buildinfo/`, `pkg/v1/proc/buildinfo/`
- Issues #272, #273, #276; kitsunium/statusline#3
