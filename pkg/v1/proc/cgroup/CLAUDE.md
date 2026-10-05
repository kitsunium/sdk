<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/proc/cgroup/

## Purpose

Thin **public facade** over `internal/service/proc/cgroup` (ADR 0016): create and
manage cgroup v2 control groups to confine a process tree. Consumers import this
package; the service and `core/proc` stay internal.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Group` | type alias | `= coreproc.Group` port (SetMemoryMax/SetCPUMax/SetPidsMax/SetIOMax/Add/Kill/Freeze/Thaw/Delete) |
| `Option` | type alias | `= svccgroup.Option` functional option |
| `WithRoot(root)` | func | target a delegated sub-tree; delegates |
| `Available()` | func | honest delegation probe; delegates |
| `Create(name, opts...)` | func | makes a sub-group; delegates |
| `MustCreate(name, opts...)` | func | `Create` that panics with the typed error — a consumer's start-up opt-in |

`Group` and `Option` are **aliases**, never new named types. The functions are
one-line delegations — `MustCreate` adds only the panic; no other logic lives
here.

## README is generated

`README.md` is produced by `gomarkdoc` from the package doc comment in
`cgroup.go` (ADR 0008). Edit the doc comment, then run `make docs-readme` (or the
`//go:generate` line). Do **not** hand-edit `README.md`.

## Semantics

`Set*Max` writes the matching cgroup v2 interface file; a negative numeric value
means "no limit" (`"max"`). On Linux, `Kill` writes `cgroup.kill` and `Freeze` /
`Thaw` write `cgroup.freeze`; each returns `UnsupportedPlatform` on a kernel
without that file. `Create` returns `CgroupUnavailable` on a
non-delegated Linux host, `CgroupCreateFailed` when creation itself fails, and
`UnsupportedPlatform` where there is no backend at
all (darwin, OpenBSD, NetBSD, DragonFly, illumos, Solaris) — always check `Available()` first, and
prefer `WithRoot` to a delegated sub-tree over the top-level mount.

Windows (Job Objects) and FreeBSD (rctl) have backends of their own, mirrored
from `internal/service/proc/cgroup`: `Available()` is true there (on FreeBSD,
when the kernel carries RACCT) and `Create` returns a working `Group` whose
`Kill` works and whose `Freeze` / `Thaw` return `UnsupportedPlatform`.
`WithRoot` is a cgroup v2 path, so both accept it and ignore it. The facade suite follows the service's split —
`cgroup_other_test.go` is `!linux && !windows && !freebsd`,
`cgroup_windows_test.go` asserts the Job Object backend through the public
names, and `TestWithRootOption` asserts the ignored root where a backend has no
hierarchy. It carried a bare `!linux` and asserted the refusal on Windows until
the first Windows run of the whole suite (ADR 0095).

## Generated

`facade_gen.go` is kit's (ADR 0165): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/proc.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the package comment and the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```sh
bazel test --config=race //pkg/v1/proc/cgroup:cgroup_test
go test -race ./pkg/v1/proc/cgroup/...
```
