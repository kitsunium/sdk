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
| `cgroup.go` | which limit files to consult: mount discovery (point AND root, octal escapes decoded), `/proc/self/cgroup` membership, the translation between the two, and the ancestor walk |

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
- **A mount point is not a mount root, and both are read.** mountinfo field 3 is
  the path WITHIN the cgroup filesystem that the mount exposes. A runtime that
  bind-mounts a container's own subtree at `/sys/fs/cgroup` — `--cgroupns=host`
  — reports that subtree there while `/proc/self/cgroup` still names the full
  path, so joining them verbatim repeats the subtree and names a file no cgroup
  answers to: five of six candidates absent, measured on a live 6.12 kernel.
  `underMountRoot` translates. A root ABOVE the cgroup namespace root is
  rendered by the kernel with `..` components and `path.Clean` folds those to
  `/`, which is the identity. A mount exposing a subtree this process is not in
  contributes NOTHING: the caller takes a minimum, so a joined path that happens
  to exist would let a stranger's cap win.
- **mountinfo is escaped and `/proc/<pid>/cgroup` is not.** `mangle_path` encodes
  space, tab, newline and backslash in mountinfo's path fields as `\040`,
  `\011`, `\012` and `\134`. `unmangleMountinfoPath` decodes them, and it runs
  on mountinfo alone — a cgroup named `probe test.scope` reads back from
  `/proc/<pid>/cgroup` with a literal `0x20`, so decoding both would corrupt a
  name that legitimately contains a backslash.
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
- **The share multiplies before dividing, up to a measured bound.** Dividing
  first discards up to 99 bytes of the allowance before taking the share, which
  at the floor declines a cap the exact value accepts — 74,565,405 bytes derives
  67,108,860 one way and exactly the 67,108,864 floor the other. Above
  `math.MaxInt64 / 90` the product wraps, so `deriveLimit` reverses the order
  there and only there. That branch is reachable: `parseV1Limit` accepts up to
  `1<<62`, 45× the ceiling. `int64` is 64 bits on every Go platform, `linux/386`
  included, so the bound is a value range and not an architecture.
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
