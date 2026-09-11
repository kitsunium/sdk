# internal/service/proc/reaper/

## Purpose

The OS implementation of the `core/proc.Reaper` port (ADR 0016): a PID1 /
subreaper zombie collector. On Unix it installs an `os/signal` SIGCHLD handler
and drains every reapable child with a non-blocking `syscall.Wait4(-1, …,
WNOHANG, …)` loop until `ECHILD`. Off Unix it degrades to a no-op so the package
links and runs everywhere. **Stdlib-only** (`os`, `os/signal`, `sync`,
`syscall`) + `internal/kernel/errs` — no `golang.org/x/sys`.

## Contents

| File | Build tag | Role |
|---|---|---|
| `reaper.go` | (all) | `Option`/`config` surface; `WithOnReap`; `resolve` |
| `reaper_unix.go` | `unix` | `unixReaper`, `New`, `Start`/`Stop`/`loop`, `ReapOnce`, `drain`/`drainResult`/`classifyWaitErr`, `LastError`, `IsPID1` |
| `subreaper_linux.go` | `linux` | `SetChildSubreaper` via `prctl(PR_SET_CHILD_SUBREAPER, 1)` |
| `subreaper_other.go` | `unix && !linux` | `SetChildSubreaper` → `UnsupportedPlatform` (no prctl on darwin/bsd) |
| `reaper_other.go` | `!unix` | no-op `noopReaper`, `New`, `Start`/`Stop`/`ReapOnce`, `SetChildSubreaper` → `UnsupportedPlatform`, `IsPID1` → false |

No `codes.go` / `errors.go`: the package mints no codes. It returns the central
`core/proc` sentinels — bare `UnsupportedPlatform`, and `errs.Wrap` of a syscall
cause restating the exact `ReapFailed` / `SubreaperFailed` fields.

## Why the three-way platform split

`syscall.Wait4` and SIGCHLD exist on every Unix, so the reaping **loop** is
tagged `unix`. `prctl(PR_SET_CHILD_SUBREAPER)` is **Linux-only** (constant 36,
not exported by `syscall`), so `SetChildSubreaper` is split into a `linux` real
implementation and a `unix && !linux` stub. The `!unix` file carries the entire
no-op reaper plus its own `SetChildSubreaper`. The four tag sets are disjoint, so
exactly one definition of each exported symbol compiles per GOOS.

## Behaviour

- **Start** — idempotent; subscribes to SIGCHLD inside the loop goroutine (so
  `signal.Notify` and `defer signal.Stop` stay paired) and drains on every
  signal. An initial drain catches children that exited before subscription.
- **Stop** — closes `done` (guarded by a per-cycle `sync.Once`), the loop runs a
  final drain, detaches the handler, and closes `stopped`; Stop blocks on
  `stopped` so no goroutine and no zombie outlives it. Safe without a prior
  Start and idempotent.
- **ReapOnce** — one non-blocking sweep returning the count; safe to call
  concurrently with the loop (Wait4 is kernel-serialised).
- **Drain semantics** — `pid>0` counted; `pid==0`/`ECHILD` end the sweep
  cleanly; `EINTR` retries; any other errno → wrapped `ReapFailed`.
- **LastError** — exposes the most recent background-sweep error (RWMutex
  guarded) since the loop cannot return one to a caller.

## Performance — see `BENCH.md`

`ReapOnce` on an idle supervisor is **283 ns and ZERO allocations** — one
`wait4(-1, WNOHANG)` returning `ECHILD`. There is no polling frequency at which
this package's own code becomes the cost; `WithOnReap` adds 5 ns and can be wired
unconditionally.

A `Start`/`Stop` cycle is **86 µs — 300× a sweep** — and a CPU profile puts 57 %
of it in `runtime.futex` under the scheduler and 9 % in `runtime.ensureSigM`. That
is the price of the contract (`Stop` blocks until the loop goroutine is gone, the
SIGCHLD subscription is detached and a final drain has run), not an inefficiency
in it. **A reaper is a per-process object**: construct and `Start` it once, not per
test case or per request. `IsPID1` is a real `getpid(2)` (119 ns), deliberately
uncached — hoist it, do not loop on it.

## Do NOT

- Treat `ECHILD` as an error — it is the normal "drained" terminus.
- Add codes here — all 22 proc codes are central in `internal/core/proc`.
- Call `prctl` outside `subreaper_linux.go`; it does not exist elsewhere.

## Verification

```sh
bazel test --config=race //internal/service/proc/reaper:reaper_test
# Fallback
cd internal/service && GOWORK=off go test -race ./proc/reaper/...
```

Privileged behaviour (subreaper reparent-and-reap) is gated: if
`PR_SET_CHILD_SUBREAPER` is unavailable the acceptance test asserts the typed
`SubreaperFailed` contract and `t.Skip`s the behavioural half.
