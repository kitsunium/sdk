# cgroup (internal/service/proc/cgroup)

cgroup v2 control-group management implementing `core/proc.Group`. Internal
service implementation behind the public `pkg/v1/cgroup` facade — consumers
import the facade, not this package.

## API

```go
func Available() bool
func Create(name string, opts ...Option) (coreproc.Group, error)
func WithRoot(root string) Option
```

`Available` confirms the unified cgroup v2 hierarchy is mounted **and** delegated
(writable) to the caller. `Create` makes a sub-group; the returned `Group`
exposes `SetMemoryMax` / `SetCPUMax` / `SetPidsMax` / `SetIOMax` / `Add` /
`Delete`. A negative numeric ceiling writes the kernel literal `"max"`.

## Errors

All typed `core/proc` sentinels — match with `errs.HasCode`:

| Condition | Code |
|---|---|
| Hierarchy absent / not delegated / read-only | `CodeCgroupUnavailable` |
| `mkdir` failed (other than a delegation denial) | `CodeCgroupCreateFailed` |
| Controller / `cgroup.procs` write failed | `CodeCgroupWriteFailed` |
| `rmdir` failed (group still populated) | `CodeCgroupDeleteFailed` |
| Called off Linux | `CodeUnsupportedPlatform` |

## Platform

Three native backends implement the `Group` port:

- **Linux** — cgroup v2 (`/sys/fs/cgroup`). Unprivileged / non-delegated hosts
  degrade to `CgroupUnavailable` without panicking.
- **Windows** — Job Objects (memory / CPU / pids caps; IO + Freeze/Thaw return
  `UnsupportedPlatform`).
- **FreeBSD** — rctl (per-process memory / CPU rules; pids / IO + Freeze/Thaw
  return `UnsupportedPlatform`).

On every other GOOS (darwin, OpenBSD, NetBSD, DragonFly) `Available()` is
`false` and `Create` returns `UnsupportedPlatform`.
