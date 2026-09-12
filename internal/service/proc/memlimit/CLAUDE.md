# internal/service/proc/memlimit/

## Purpose

Derives the Go runtime **soft memory limit** from the control-group allowance
already governing this process, and installs it via `runtime/debug.SetMemoryLimit`
(ADR 0075). **Stdlib-only** plus `internal/core/proc` for the result value — no
`golang.org/x/sys`, no syscall: every input is a file under `/proc` or
`/sys/fs/cgroup`.

It is the READ direction of the cgroup subject. The sibling `cgroup` package
WRITES a control group to bound a child process; this one reads the cap already
bounding *us*.

## Contents

| File | Role |
|---|---|
| `memlimit.go` | `Apply` + the injected-seam `applyFrom`, the limit-file parsers, and every threshold constant |
| `cgroup.go` | which limit files to consult: mount discovery, `/proc/self/cgroup` membership, and the ancestor walk |

## Why-this-shape

- **Ancestors are consulted, not just the process's own cgroup.** A restrictive
  parent bounds the process just as effectively. Reading only the mount root was
  the original defect: under systemd, and under any runtime that does not use
  cgroup namespaces, the root reports `max` while the real cap sits several
  levels down — so the feature silently disabled itself exactly where it was
  needed. `TestApplyFrom_NestedCgroup` pins all four shapes.
- **Both hierarchies are consulted.** A hybrid host mounts v1 and v2 at once and
  either can bind, so the tightest real cap across all candidates wins rather
  than the first one found.
- **Mount points are discovered, not assumed.** Limits are read relative to the
  mount point; hard-coding `/sys/fs/cgroup` reads nothing on a host that mounts
  the hierarchy elsewhere. `/proc/self/mountinfo` answers it, with the
  conventional locations as the fallback when it is unreadable.
- **`memory` must match exactly.** A v1 controller list is comma-separated, so
  `memory+swap` must not match `memory` — `slices.Contains` over the split list,
  never `strings.Contains`.
- **An EMPTY `GOMEMLIMIT` is not an override.** The Go runtime ignores an empty
  value and stays unbounded, so treating mere presence as an operator decision
  would disable this feature for an env var that does nothing. Presence *and* a
  non-blank value is the test.
- **90% headroom, and a 64 MiB floor.** The remainder covers what counts against
  the cgroup but sits outside runtime accounting — binary image, mapped files, C
  allocations, kernel memory held on our behalf. Below the floor the derivation
  declines rather than thrashing the collector without averting the kill.
- **Three seams, injected.** `applyFrom` takes `lookup`, `readFile` and
  `setLimit` so every branch is driven without a real cgroup filesystem and
  without mutating the process-wide runtime limit under `t.Parallel()`.

## Do NOT

- Call `debug.SetMemoryLimit` anywhere but through the injected `setLimit` seam —
  the limit is process-wide and leaks across parallel tests.
- Add a build tag. Control groups are a Linux facility, but the absence of the
  files IS the non-Linux answer: every read simply fails and the derivation
  reports `MemorySourceUnconstrained`. A tag would buy nothing and cost a
  platform matrix.
- Return an error. No outcome here is a failure: an unreadable cgroup file on a
  laptop is the normal state, not a fault. The outcome is a value, and
  `MemorySource` is what tells the four apart.

## Verification

```sh
bazel test --config=race //internal/service/proc/memlimit:memlimit_test
go test -race ./internal/service/proc/memlimit/...
```
