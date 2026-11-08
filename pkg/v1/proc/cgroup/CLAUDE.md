<!-- updated: 2026-11-08T12:00:00Z -->
# pkg/v1/proc/cgroup/

## Purpose

Thin **public facade** over `internal/service/proc/cgroup` (ADR 0016): create and
manage cgroup v2 control groups to confine a process tree. Consumers import this
package; the service and `core/proc` stay internal.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Group` | type alias | `= coreproc.Group` port (SetMemoryMax/SetCPUMax/SetPidsMax/SetIOMax/Add/Kill/Freeze/Thaw/Delete) |
| `GroupReader`, `GroupStat`, `MemoryEvents`, `IOStat` | aliases | the read half of a `Group` and its values (#932) |
| `Probe`, `ProbeResult` | func, alias | what the caller may do under a root — a delegated subtree included (#1097) |
| `Membership(dir)` | func | a group's directory (`GroupTree.Path`) as `/proc/<pid>/cgroup` names it — what `process.AttachScope`'s `Cgroup` takes (#734); `UnsupportedPlatform` off Linux |
| `WithControllers(ctrls...)`, `Controller`, `ControllerCPU` … `ControllerMisc` | func, alias, constants | the controllers enabled from the root down to a new group (#727) |
| `GroupTuner`, `GroupTree`, `GroupWatcher`, `GroupSignaler`, `GroupEvents` | aliases | the Linux group's siblings: typed v2 setters; `Path` / `EnableControllers` / `Delegate` / `DeleteTree`; the `cgroup.events` watch and its value; `Signal` / `Contains` (#727) |
| `JobLimiter` | alias | the Windows group's sibling: `SetJobMemoryLimit`, the whole job's cap (#727) |
| `Option` | type alias | `= svccgroup.Option` functional option |
| `WithRoot(root)` | func | target a delegated sub-tree; delegates |
| `Available()` | func | honest delegation probe; delegates |
| `Create(name, opts...)` | func | makes a sub-group; delegates |
| `MustCreate(name, opts...)` | func | `Create` that panics with the typed error — a consumer's start-up opt-in |

`Create` takes a nested name on Linux (`"unit/main"`), never leaving the
root (#727). The sibling ports are asserted on the `Group` `Create` returns,
as `GroupReader` is: a platform without the mechanism has none, and the
failed assertion is its refusal. `cgroup_linux_external_test.go` asserts the
four Linux siblings and the controllers on a fake root through the public
names.

`Group` and `Option` are **aliases**, never new named types. The functions are
one-line delegations — `MustCreate` adds only the panic; no other logic lives
here.

## README is generated

`README.md` is written by `tools/genindex` from the committed `docs/api`
(`make docs-readme`, ADR 0167). Do **not** hand-edit `README.md`. The package
comment is in `doc.go`, which kit writes from the design (`design/proc.yaml`):
edit the design, run `kit gen`, then `make api` and `make docs-readme`.

## Semantics

`Set*Max` writes the matching cgroup v2 interface file; a negative numeric value
means "no limit" (`"max"`). On Linux, `Kill` writes `cgroup.kill` and `Freeze` /
`Thaw` write `cgroup.freeze`; each returns `UnsupportedPlatform` on a kernel
without that file. After a `Kill`, Linux 6.12 SIGKILLs every child later
cloned into the group (`Spec.CgroupPath`) at its fork: kill a group you will
not spawn into again, signal one you will (`GroupSignaler.Signal`).
`GroupReader.Members` lists the group and every group beneath it. `Create` returns `CgroupUnavailable` on a
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

`gen_facade.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/proc.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `gen_facade.go`: the hand-written files keep the declarations of their own, and `doc.go` — kit's too (ADR 0167) — the package comment. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `gen_facade.go` edited by hand.

## Verification

```sh
bazel test --config=race //pkg/v1/proc/cgroup:cgroup_test
go test -race ./pkg/v1/proc/cgroup/...
```
