# ADR 0188 — the operator's switches only restrict, and State says what the server runs without

- **Status**: Accepted
- **Date**: 2026-10-25
- **Accepted**: 2026-10-25, on the owner's decisions of 2026-10-24 for the
  unified network work, R1 (no restart in process: a drained ring is degraded,
  a group whose every ring drained stops `Serve` and fails liveness after a
  named grace) and R5 (the environment only restricts), which
  [ADR 0187](0187-the-handler-type-chooses-the-engine.md) §D8 recorded and
  left to their own change. It can be revisited while the SDK is v0
  ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers, owner
- **Amended by**: [ADR 0189](0189-a-kernel-capability-is-probed-reported-and-switched-off-by-name.md) — §D1 (the registry gains the kernel
  capabilities) and §Deferred (they join it)
- **Amends**: [ADR 0187](0187-the-handler-type-chooses-the-engine.md) §D8
  (the switches it names, made concrete: their registry, when they are read,
  what they do to each kind of group) and its §Consequences (`StateValue`
  gains four fields and `Degraded` reads one of them)
- **Related**: [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md)
  §D5 (the backends) and §D8 (`State`), [ADR 0060](0060-sdk-health-domain.md)
  (readiness is not liveness), [ADR 0095](0095-windows-runs-every-test-and-gates.md)
  (a degradation `State` reported wrong), [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md)
  (design first)

## Context

ADR 0187 §D8 decided that the operator may lower the engine a group runs on
and never raise it, and named two variables for it. It did not say where their
vocabulary lives, when they are read, what an unknown word does, or how a
replica tells an orchestrator that a loop group it runs has stopped accepting.
Four things were missing for the next steps of the programme, which turn
kernel capabilities on by default and must leave an operator a way back:

- **A vocabulary nobody can drift from.** A token the parser accepts and the
  documentation does not name, or the other way round, is a switch an operator
  reaches for during an incident and finds dead.
- **Reading before probing.** The io_uring probe sets up rings; an operator who
  switched io_uring off because the kernel misbehaves under it must not have it
  set up anyway.
- **One place that says what the server runs without.** `State` carried a
  `Degraded` flag per listener and per loop group, and nothing for an unknown
  token, a drained ring or a stalled group.
- **A stall the process cannot recover from.** The io_uring accept watchdog
  drains a ring it judges sick. While other rings accept, the group serves
  less; once every ring drained, it serves nothing and restarting the engine in
  process is ruled out (R1).

## Decision

### D1 — The capability registry is in the design

`design/net.yaml` declares `Capability` (a string) and its four values, the
lattice top first: `CapabilityIOURing` (`iouring`) > `CapabilityEpoll`
(`epoll`) > `CapabilityPortable` (`portable`) > `CapabilityGoroutine`
(`goroutine`), in two blocks: the engines an operator may switch off
(`iouring`, `epoll`) and the floors, which `KITSUNIUM_NET_ENGINE` may name and
nothing switches off. `EnvEngine` and `EnvDisable` name the two variables. The
parser's table in `net/server` is held to the design's blocks by a test that
reads `design/net.yaml`; the CI check of D6 reads the same file. A capability a
later step adds — an optional kernel feature — is added to the design first,
and both follow.

### D2 — Read once, by `New`, before any probe

`net/server.New` reads both variables through an injected lookup
(`os.LookupEnv` in production, a map in tests) after its options and caches the
result in the `Server`. Nothing reads the environment again: a group declared
later, a `Start`, a probe all see the same answer.

- An empty value, or one of blanks, is "not set".
- `KITSUNIUM_NET_ENGINE=<token>` caps every group at that rank.
- `KITSUNIUM_NET_DISABLE=<token>,…` switches each listed engine off; empty items
  say nothing, a repeat counts once.
- A token outside the registry, in either variable, or a floor named in
  `KITSUNIUM_NET_DISABLE`, caps the lattice at `portable`, with no optional
  capability, and is reported as a fault (D4). Tokens match exactly: `EPOLL` is
  unknown.

### D3 — The effective backend is min(code, environment)

For a loop group, the backend is the highest one at or below the code's choice
that the environment allows; the code's `auto` is io_uring's rank.

- `auto` with both native backends allowed keeps the leaf's own io_uring then
  epoll; with one ruled out, the leaf is asked for the other alone; with both
  ruled out, the group runs on the portable backend. Under `auto` a native
  failure still falls back to portable, reported (ADR 0186 §D5).
- An explicit backend lowered by the environment runs on the lower one, and a
  failure there refuses `Start`, as the explicit backend's own would.
- **A backend the environment rules out is never probed.** The leaf is asked
  for `epoll` explicitly, or the portable backend starts without the leaf, so
  `io_uring_setup` is never called; `State.Loops[i].ProbeStage` reads `epoll`.
- `goroutine` for a loop handler is the portable backend, the lowest engine
  that can serve one; for an `http1` handler that is the codec on portable
  (ADR 0187 §D5), the same path. `epoll` off Linux is `portable`, since off
  Linux every loop group already is.
- An explicit native backend the platform cannot run is refused first,
  whatever the environment: the environment never turns a refusal into a
  group served elsewhere.
- A goroutine-engine group is the floor already: the environment does nothing
  to it. **The environment never touches TLS, `Plaintext` or any limit.**
- A group the environment lowered is overridden, not degraded: it is neither a
  fault nor a fallback.

### D4 — `State` says what the server runs without

`StateValue` gains four fields:

- **`Override`** (`OverrideValue`): `Source` (`environment` when a variable is
  set), `Raw` (what was read, `NAME=value` each), `Applied` (the highest engine
  the environment lets a group run on) and `Disabled`.
- **`Faults`** (`[]FaultValue`, each a `Source`, a `Group` and a `Reason`):
  every degraded listener and loop group, as their own rows already say; each
  unknown token; and, while serving, each ring its watchdog drained while
  others accept (`ring`), and each group every ring of which is drained
  (`stall`). A fault about a drained ring names the runbook:
  `KITSUNIUM_NET_DISABLE=iouring` before restarting. Nothing is read as a fault
  during a drain, which stops every ring on purpose.
- **`Fallbacks`** (`FallbacksValue`): how many groups started on a lower engine
  than their code asked for because the better one failed, and the last one's
  group, reason and time.
- **`Platform`** (`PlatformValue`): `GOOS`, `GOARCH`, the kernel's release as
  the platform reports it — uname on Linux, `kern.osrelease` on darwin and the
  BSDs, `GetVersion` on Windows, empty on illumos and Solaris where the
  standard library reads none — and the toolchain.

**`Degraded()` aggregates `Faults`.** It is true when `Faults` is not empty, and
still when a `Listeners` or `Loops` row says `Degraded`, so a `StateValue` built
by hand answers as before. The migration for a caller is to read `Faults`
instead of walking the two slices: every degradation they report is in it, with
what they could not say.

### D5 — Readiness and liveness are the server's own (R1)

`Server.RegisterHealth(h)` registers two checks named `net/server` on a
`core/app/health` registry, owned by the server, built from `State`:

- **Readiness**: ready while `PhaseServing` with no stalled loop group;
  `NotStarted` before, `ServerClosed` once the drain began, `LoopGroupStalled`
  for a stalled group. A degraded server is ready: it serves.
- **Liveness**: process-local, as ADR 0060 requires. It fails with
  `LoopGroupStalled` once a group has stayed stalled for the stall grace,
  `loopStallGrace`, 30 seconds on the engine's clock.

A watcher reads the loop groups every second while the server serves. The
first group seen stalled is recorded: `Serve` drains and returns
`LoopGroupStalled` (`0.2.11.59`) naming it, at once, and the grace counts from
then for a process that runs without `Serve`. A process restarted with the
same environment stalls the same way; the runbook is the switch D4 names.
**A receive that never completes on a live ring is not watched**: only the
accept watchdog drains a ring, and a peer that stops sending looks, to the
loop, like a peer that has nothing to say — `IdleTimeout` is the bound for it.

### D6 — The `net:default-change` label is gated at the merge

A pull request that changes a default of the network engines carries the
`net:default-change` label, and its body holds an `Override:` block — a line
`Override:`, then `KITSUNIUM_NET_ENGINE=<token>` and/or
`KITSUNIUM_NET_DISABLE=<token>,…`, each at most once — saying how an operator
gets the previous behaviour back. `scripts/ci/check-net-default-change.sh`
holds the block to the registry of D1 (an `ENGINE` token is any capability, a
`DISABLE` token one an operator may switch off), run by
`net-default-change.yml` on the pull request and in the merge queue, where the
body is read through the API (ADR 0187 §D10). The release only renders the
block back: `release-notes.sh default-changes` puts each labelled pull
request's block in the notes, and judges nothing.

## Consequences / Semantics

- `core/net` gains `Capability` and its four constants, `EnvEngine`,
  `EnvDisable`, `OverrideValue`, `FaultValue`, `FallbacksValue`,
  `PlatformValue`, four `StateValue` fields and `LoopGroupStalled`
  (`0.2.11.59`). `net/server` gains `Server.RegisterHealth` and imports
  `core/app/health`. `pkg/v1/net/server` re-exports them (`Capability`,
  `Override`, `Fault`, `Fallbacks`, `Platform`).
- `Serve` returns on a stall as well as on its context.
- A process with neither variable set, no degraded listener and no loop group
  sees no difference but the new, zero or empty, fields.
- The `net-default-change` status is a new check; making it required is a
  repository setting.

## Breaking changes

Deliberate, and allowed while v0 (ADR 0040):

1. **`StateValue.Degraded()` is true for a fault no row carries**: an unknown
   environment token, a drained ring, a stalled group. A caller asserting "not
   degraded" with such a token set now sees `true`. Old shape: `Degraded()`
   walked `Listeners` and `Loops` only. New shape: it also reads `Faults`.
2. **`Serve` can return without its context ending**, with `LoopGroupStalled`,
   after the drain. Old shape: it returned only once `ctx` was done (or `Start`
   failed).

## Alternatives considered

- **Read the environment at `Start`.** Rejected: `State` before `Start` would
  not say what the operator asked, and two `Start`s of a test would each read.
- **Unknown token ignored.** Rejected (R5): an operator who mistyped
  `iouring` during an incident would keep io_uring with no sign; the cap at
  portable is the restrictive reading, and the fault names the token.
- **Restart a stalled group's engine in process.** Rejected (R1): the ring was
  drained because the kernel stopped answering its accepts; a new ring on the
  same kernel, same flags, meets the same answer, and a loop that restarts
  itself hides the cause behind a counter.
- **A field per degradation kind instead of `Faults`.** Rejected: each new
  kind — a capability the probe could not confirm, P3's — would be a new field
  every consumer must learn; one list with a source is read once.
- **Gate the label at the release.** Rejected (ADR 0187 §D10): every merge
  publishes, so a gate after the merge holds nothing.

## Deferred

- **Optional capabilities** (multishot receive, provided buffers, direct
  descriptors, `EPOLLEXCLUSIVE`, the class B features): each joins the registry
  when its step (P3) makes it a capability the code can switch off, with its
  own `net:default-change` pull request.
- **Watching a stalled receive**: a deadline per ring for a receive armed and
  never completed, if a measurement ever shows one the idle timeout misses.
- **Requiring the `net-default-change` status** in the ruleset, a repository
  setting.

## References

- [ADR 0187](0187-the-handler-type-chooses-the-engine.md) §D8, §D10.
- [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md) §D5, §D8.
- [ADR 0060](0060-sdk-health-domain.md); [ADR 0095](0095-windows-runs-every-test-and-gates.md); [ADR 0040](0040-changing-a-published-shape-while-v0.md).
- Impl: `design/net.yaml` (the registry), `internal/core/net/`
  (`capability.go`, `state_report.go`, `state.go`),
  `internal/service/net/server/` (`operator.go`, `stall.go`, `health.go`,
  `platform*.go`), `scripts/ci/check-net-default-change.sh`,
  `.github/workflows/net-default-change.yml`,
  `scripts/release/release-notes.sh`.
