# pkg/v1/cgroup/

## Purpose

Thin **public facade** over `internal/service/proc/cgroup` (ADR 0016): create and
manage cgroup v2 control groups to confine a process tree. Consumers import this
package; the service and `core/proc` stay internal.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Group` | type alias | `= coreproc.Group` port (SetMemoryMax/SetCPUMax/SetPidsMax/SetIOMax/Add/Delete) |
| `Option` | type alias | `= svccgroup.Option` functional option |
| `WithRoot(root)` | func | target a delegated sub-tree; delegates |
| `Available()` | func | honest delegation probe; delegates |
| `Create(name, opts...)` | func | makes a sub-group; delegates |

`Group` and `Option` are **aliases**, never new named types. The functions are
one-line delegations — no logic lives here.

## README is generated

`README.md` is produced by `gomarkdoc` from the package doc comment in
`cgroup.go` (ADR 0008). Edit the doc comment, then run `make docs-readme` (or the
`//go:generate` line). Do **not** hand-edit `README.md`.

## Semantics

`Set*Max` writes the matching cgroup v2 interface file; a negative numeric value
means "no limit" (`"max"`). `Create` returns `CgroupUnavailable` on a
non-delegated Linux host and `UnsupportedPlatform` where there is no backend at
all (darwin, OpenBSD, NetBSD, DragonFly) — always check `Available()` first, and
prefer `WithRoot` to a delegated sub-tree over the top-level mount.

Windows (Job Objects) and FreeBSD (rctl) have backends of their own, mirrored
from `internal/service/proc/cgroup`: `Available()` is true there and `Create`
returns a working `Group`. `WithRoot` is a cgroup v2 path, so both accept it and
ignore it. The facade suite follows the service's split —
`cgroup_other_test.go` is `!linux && !windows && !freebsd`,
`cgroup_windows_test.go` asserts the Job Object backend through the public
names, and `TestWithRootOption` asserts the ignored root where a backend has no
hierarchy. It carried a bare `!linux` and asserted the refusal on Windows until
the first Windows run of the whole suite (ADR 0095).

## Verification

```sh
bazel test --config=race //pkg/v1/cgroup:cgroup_test
go test -race ./pkg/v1/cgroup/...
```
