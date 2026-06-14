# rlimit (internal/service/proc/rlimit)

Per-process resource ceilings via `setrlimit(2)` / `prlimit64(2)`. Internal
service implementation behind the public `pkg/v1/rlimit` facade — consumers
import the facade, not this package.

## API

```go
func Apply(pid int, limits map[coreproc.Resource]coreproc.LimitValue) error
func PrepareSysProcAttr(limits map[coreproc.Resource]coreproc.LimitValue) error
```

- `Apply` sets each soft/hard pair. `pid == 0` targets the calling process
  (`setrlimit`); any other pid targets that process (`prlimit64`, needs
  `CAP_SYS_RESOURCE`).
- `PrepareSysProcAttr` validates a limit set without any syscall (Go's
  `SysProcAttr` has no rlimit field; limits are applied post-fork).

## Errors

All typed `core/proc` sentinels — match with `errs.HasCode`:

| Condition | Code |
|---|---|
| Resource has no `RLIMIT_*` mapping | `CodeUnknownResource` |
| `setrlimit`/`prlimit64` failed | `CodeRlimitFailed` |
| Called off Linux | `CodeUnsupportedPlatform` |

## Platform

Linux only; the `!linux` build returns `UnsupportedPlatform`. `RLIMIT_NPROC` and
`RLIMIT_MEMLOCK` are restated from the kernel generic ABI (absent from Go's
`syscall`).
