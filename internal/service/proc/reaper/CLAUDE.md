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
