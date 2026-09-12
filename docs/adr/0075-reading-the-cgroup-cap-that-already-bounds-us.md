# ADR 0075 — the SDK reads the cgroup cap that already bounds this process, not only the ones it writes for others

- **Status**: Accepted
- **Date**: 2026-09-12
- **Deciders**: SDK maintainers
- **Related**: [ADR 0016](0016-sdk-process-supervision-domain.md) (the proc domain), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a zero value is never an inert policy), [ADR 0001](0001-sdk-go-multimodule-layout.md) (the four layers)

## Context

The `proc` domain covers control groups in one direction only. `cgroup` CREATES a
control group, puts a pid in it and writes `memory.max`; `rlimit` applies a
`setrlimit(2)` ceiling to a process. Both act on somebody else, or on a limit the
kernel then enforces by failing an allocation.

Nothing in the SDK reads the cap that already bounds the calling process, and the
Go runtime does not do it either. `GOMAXPROCS` has been cgroup-aware since Go
1.25; the memory limit never was. The consequence is not subtle: a service in a
512 MiB container grows its heap past the cap and is SIGKILLed by the kernel,
where a soft limit would instead have pushed the collector to work harder. The
process gets no signal it can handle, no unwind, no log line — the kernel simply
removes it.

Every SDK consumer that ships in a container has this failure mode, and the fix
is the same six-line derivation every time: read the allowance, take a
percentage, call `debug.SetMemoryLimit`. That is the definition of something that
belongs in the SDK rather than in each consumer.

The implementation this ADR adopts is not new code. It comes from
`kodflow/ktn-linter`'s `pkg/memlimit`, where it was written for exactly this
failure mode and has been carrying its own regression suite; the versement is the
subject of the linter→SDK convergence work.

## Decision

**`proc` gains a third direction: READ the cap governing this process.**
`internal/service/proc/memlimit` derives the Go soft memory limit from the
control-group allowance and installs it; `pkg/v1/memlimit` is the facade.

Four decisions inside it are load-bearing and are restated here because each one
was a defect before it was a rule:

1. **Ancestors count, and the mount root is not the answer.** Limits belong to
   cgroup *directories*. Under systemd, and under any runtime that does not use
   cgroup namespaces, the root reports `max` while the real cap sits levels down —
   so reading the root alone disabled the feature precisely where it was needed.
   The derivation resolves `/proc/self/cgroup`, then walks outward, and takes the
   minimum: a restrictive parent bounds us just as effectively as our own cgroup.

2. **Both hierarchies count.** A hybrid host mounts v1 and v2 at once and either
   can bind. Stopping at a v2 `max` ignores a v1 memory controller that genuinely
   caps the process.

3. **An empty `GOMEMLIMIT` is not an operator decision.** The runtime ignores an
   empty value and stays unbounded. Treating mere presence as an override would
   disable the feature for an environment variable that does nothing — so
   presence *and* a non-blank value is the test.

4. **The outcome is a value, not an error, and it names itself.** Not being in a
   container is the normal state on a developer laptop; an unreadable cgroup file
   is not a fault. So `Apply` cannot fail. But "declined" alone is unusable for
   diagnosis: an operator who set `GOMEMLIMIT`, a host with no cap, and a cap too
   tight to honour are three different situations and only the first is
   intentional. `MemoryLimitValue` carries a `MemorySource` that separates them,
   and per ADR 0031 the zero of that enum is left unminted — a `MemorySource`
   nobody set renders `"unknown"` rather than defaulting to one of the four.

The public surface is one function returning one value:

```go
applied := memlimit.Apply()
if applied.Applied() {
    // applied.Limit bytes, derived from applied.Allowance
}
```

## Consequences

- Every consumer gets the container fix with one call at start-up, and the call
  is safe on any host: off Linux no limit file is readable, so it reports
  `MemorySourceUnconstrained` and leaves the runtime default in place. No build
  tag, no platform matrix.
- The limit is SOFT and partial, and the documentation says so in three places
  rather than implying a guarantee. It covers what the Go runtime maps and
  manages — heap, goroutine stacks, runtime metadata — and excludes the binary
  image, C allocations, `syscall.Mmap` mappings, kernel memory held on our
  behalf, and subprocesses entirely. Ninety percent of the allowance goes to the
  runtime and the remaining tenth covers those. **This reduces OOM pressure; it
  does not eliminate it.**
- Below a 64 MiB derived limit the derivation declines. A cap that small cannot
  host a real working set, and holding the runtime under it would spin the
  collector continuously without averting the kill.
- `proc` now answers three questions with three packages, and the distinction has
  to stay legible: `cgroup` writes a bound for a child, `rlimit` sets a ceiling
  the kernel enforces, `memlimit` reads the bound already on us and tunes the
  collector. Nothing in `memlimit` enforces anything.

## Alternatives considered

- **Leave it to consumers.** It is six lines — but it is six lines that were
  wrong twice in the source implementation (mount root only; v2 only), and both
  bugs were silent: the feature disabled itself and nothing reported it. A
  defect whose symptom is "the safeguard quietly did nothing" is exactly what a
  shared implementation is for.
- **Take a third-party dependency.** `pkg/go.mod` is deliberately dep-light so
  consumers of `pkg/v1` inherit nothing. The derivation is stdlib-only — every
  input is a file under `/proc` or `/sys/fs/cgroup` — so a dependency would buy
  nothing and cost the whole consumer graph.
- **Fold it into `cgroup` as a `Read` verb.** `cgroup` is about groups the caller
  creates and owns, and its `Group` handle is the thing you write through.
  Reading the cap on the current process needs no handle, creates nothing, and
  works where the caller has no permission to create anything. One package, one
  direction.
- **Return `(int64, bool)`, as the source implementation did.** Simpler to call,
  and it is what the linter used. It also made the three declining outcomes
  indistinguishable, which is the shape a caller cannot log usefully. The
  ergonomics are preserved by `Applied()`; the information is preserved by
  `Source`.
