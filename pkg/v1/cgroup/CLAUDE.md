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
non-delegated host and `UnsupportedPlatform` off Linux — always check
`Available()` first, and prefer `WithRoot` to a delegated sub-tree over the
top-level mount.

## Verification

```sh
bazel test --config=race //pkg/v1/cgroup:cgroup_test
go test -race ./pkg/v1/cgroup/...
```
