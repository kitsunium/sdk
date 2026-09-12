# memlimit (internal/service/proc/memlimit)

Derives the Go runtime soft memory limit from the control-group allowance
already governing this process, and installs it. Internal service implementation
behind the public `pkg/v1/memlimit` facade — consumers import the facade, not
this package.

## API

```go
func Apply() coreproc.MemoryLimitValue
```

`Apply` reads the tightest cgroup memory cap governing this process, keeps 10%
headroom, and calls `runtime/debug.SetMemoryLimit`. It returns what it read
(`Allowance`), what it installed (`Limit`), and what decided the outcome
(`Source`).

## Errors

None. `Apply` cannot fail and cannot panic: not being in a container is the
normal state on a developer machine, not a fault. Every outcome is a value, and
`coreproc.MemorySource` separates the four:

| Source | Meaning |
|---|---|
| `MemorySourceCgroup` | a cap was read and a limit installed — the only outcome where `Applied()` is true |
| `MemorySourceOperator` | `GOMEMLIMIT` carried a non-empty value; the runtime already honours it |
| `MemorySourceUnconstrained` | no control group declares a cap (uncapped, or not Linux) |
| `MemorySourceBelowFloor` | a cap was read but the derived limit fell under 64 MiB, and was discarded |

## What the limit covers

The limit is **soft** and **partial**. It covers memory the Go runtime maps and
manages — heap, goroutine stacks, runtime metadata — and excludes the binary
image, C allocations, `syscall.Mmap` mappings, kernel memory held on the
process's behalf, and subprocesses entirely. The 10% held back covers those.

It therefore reduces OOM pressure rather than eliminating it.

## Platform

Control groups are a Linux facility, but no build tag is needed: off Linux no
limit file is readable, so the derivation reports `MemorySourceUnconstrained` and
leaves the runtime default in place.

## See also

- `pkg/v1/memlimit` — the public facade
- `internal/service/proc/cgroup` — the opposite direction: WRITES a control group
  to bound a child process
- `internal/service/proc/rlimit` — a `setrlimit(2)` ceiling the kernel enforces
- ADR 0075 — the decision and its limits
