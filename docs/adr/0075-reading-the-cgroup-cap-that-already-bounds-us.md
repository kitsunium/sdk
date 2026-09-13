# ADR 0075 — the SDK reads the cgroup cap that already bounds this process, not only the ones it writes for others

- **Status**: Accepted. **All three §Deferred items are CLOSED** — the mount root, the octal escapes, and the order of the 90% derivation
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

## Breaking changes

None. `memlimit` is a new capability in this change set: no existing symbol
changes shape, and nothing in the SDK imports it.

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

## Deferred

> **Status update (2026-09-13).** All three items below are **CLOSED**. They were
> deferred together on the premise that they "could not be exercised here"; that
> clause is what expired. Two of them reproduce against a live kernel in one
> `unshare` command, and all three reproduce deterministically in the existing
> unit harness, which is where the fixes are pinned. The reasoning below is left
> exactly as it was written — a status, not a decision edit
> (`docs/adr/CLAUDE.md` §Do NOT).

- ~~**A cgroup mount whose root is not `/`.** `/proc/self/mountinfo` field 3 is the
  mount's root within its filesystem, and a bind mount with a non-`/` root means
  a membership path from `/proc/self/cgroup` has to be translated relative to it
  before it names a readable file. The derivation does not do that translation.
  The failure mode is fail-safe — the candidate file is simply absent, the
  derivation reports `MemorySourceUnconstrained` and the runtime default stands —
  and the shape is rare enough that it could not be exercised here. Left as it
  came from the source implementation rather than changed untested.~~
  **CLOSED.** The shape is not rare: it is what a container runtime produces with
  `--cgroupns=host`, and
  `unshare -Urm sh -c 'mount --bind /sys/fs/cgroup/user.slice /sys/fs/cgroup'`
  builds it on a stock Linux 6.12 host. The kernel writes
  `1775 1733 0:29 /user.slice /sys/fs/cgroup … - cgroup2 cgroup2 rw`, and under
  it the shipped derivation named six candidate files of which **five were
  absent**; the one that existed was an ancestor reached by accident. Every
  candidate now exists. `underMountRoot` does the translation, and two
  measurements shape it rather than guesses. First, a mount whose root sits
  *above* the cgroup namespace root is rendered by the kernel with `..`
  components — `/../../../../..` on 6.12 — and `path.Clean` folds those to `/`,
  which is the identity translation and byte-for-byte the behaviour that shipped.
  Second, a mount exposing a subtree this process is **not** in now contributes
  **nothing** rather than a joined path: every path it could build is either
  absent or another cgroup's cap, and since the caller takes a minimum, a
  stranger's cap would win.
- ~~**Octal escapes in `/proc/self/mountinfo` are not decoded.** The kernel encodes
  space, tab, newline and backslash in the path fields as `\040`, `\011`, `\012`
  and `\134`. The parser keeps the token verbatim, so a cgroup filesystem mounted
  at a path containing one of those characters yields a candidate path no file
  answers to. Fail-safe again — the cap reads as absent — and from the source
  implementation. Recorded rather than fixed blind.~~
  **CLOSED.** `mount --bind /sys/fs/cgroup "/tmp/cg dir"` is enough: the kernel
  writes `/tmp/cg\040dir` and the shipped derivation named **six** candidates,
  **all six absent**. `unmangleMountinfoPath` decodes any three-digit octal
  triple rather than only the four the kernel emits — `mangle_path` escapes its
  own backslash as `\134`, so a literal `\040` cannot reach the decoder — and a
  marker the kernel did not write as an escape is carried through verbatim
  instead of swallowing the three bytes behind it.

  The decode runs on mountinfo and **nowhere else**, which was measured rather
  than assumed: a cgroup created as `probe test.scope` with a real process moved
  into it reads back from `/proc/<pid>/cgroup` with a literal `0x20` byte. The
  two files do not agree on escaping, so decoding both would corrupt a cgroup
  name that legitimately contains a backslash.
- ~~**The 90% derivation truncates before multiplying** (`allowance / 100 * 90`).
  Within a few bytes of the 64 MiB floor this can decline a cap the exact
  computation would have accepted — an allowance of 74,565,405 bytes derives
  67,108,860 where the floored exact value is 67,108,864. The ordering is the
  source implementation's and is deliberate against int64 overflow on a very
  large allowance. Kept, so the versement stays faithful; the divergence is
  recorded here rather than silently corrected.~~
  **CLOSED**, and the overflow the ordering defended against is real — so it is
  now defended against by a bound rather than by a rounding error everybody
  pays. `deriveLimit` multiplies first below `math.MaxInt64 / 90`
  (102,481,911,520,608,620 bytes, ~91 PiB) and divides first above it.

  The overflow is reachable, which is why the guard stays: `parseV1Limit`
  accepts any value under `1<<62`, forty-five times the ceiling, and
  `parseV2Limit` accepts up to `math.MaxInt64`. Unguarded, an allowance one byte
  past the ceiling derives **−92,233,720,368,547,757**, the largest a v1 file
  yields derives 92,233,720,368,547,757 — fifty times too small and applied
  without a word — and `math.MaxInt64` derives **0**. It is **not** an
  architecture question: `int64` is 64 bits on every Go platform, and the
  `linux/386` build in this repo's matrix computes `math.MaxInt64 / 90` and the
  wrapped product to the same values as `linux/amd64`, measured with a native
  32-bit binary. Only `int` differs there, and this derivation uses none.

  The observable change is one cap-boundary decision and up to 89 bytes of
  limit: the ADR's own 74,565,405-byte allowance now applies a 67,108,864-byte
  limit instead of declining, and a 1 GiB allowance derives 966,367,641 instead
  of 966,367,620.

## References

- [ADR 0016](0016-sdk-process-supervision-domain.md) — the `proc` domain this extends
- [ADR 0031](0031-policy-zero-values-are-never-inert.md) — why `MemorySource`'s zero is left unminted
- [ADR 0001](0001-sdk-go-multimodule-layout.md) — the four layers this is placed in
- `runtime/debug.SetMemoryLimit` — https://pkg.go.dev/runtime/debug#SetMemoryLimit
- cgroup v2 `memory.max` — https://docs.kernel.org/admin-guide/cgroup-v2.html
