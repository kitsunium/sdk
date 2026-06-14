# reaper (internal/service/proc)

OS implementation of the `core/proc.Reaper` port — a PID1 / subreaper zombie
collector. Consumers use the `pkg/v1/reaper` facade; this package is internal.

## What it does

A process that parents orphaned descendants (an init in a container, or any
supervisor that armed `SetChildSubreaper`) must reap children that reparent to
it, or they pile up as zombies and exhaust the pid space. This package wraps the
kernel mechanics:

- a SIGCHLD handler driving a non-blocking `Wait4(-1, …, WNOHANG, …)` drain loop;
- `prctl(PR_SET_CHILD_SUBREAPER, 1)` to opt a non-init supervisor into receiving
  orphaned grandchildren (Linux only);
- `IsPID1()` to detect the init role.

## Platform matrix

| GOOS | Reaping loop | SetChildSubreaper |
|---|---|---|
| linux | real SIGCHLD/Wait4 | real `prctl` |
| darwin, *bsd | real SIGCHLD/Wait4 | `UnsupportedPlatform` |
| windows, others | no-op | `UnsupportedPlatform` |

The package compiles and runs on every GOOS; only behaviour degrades.

## Lifecycle

```go
r := reaper.New(reaper.WithOnReap(func(n int) { /* observe */ }))
r.Start()        // background drain on each SIGCHLD
defer r.Stop()   // final drain + goroutine join; no zombie outlives Stop
n, err := r.ReapOnce()  // single non-blocking sweep, concurrency-safe
```

`ECHILD` is a clean end of a sweep, never an error. Any other wait error surfaces
as the central `core/proc.ReapFailed` sentinel.

## Errors

No codes are minted here. Returned errors are the central `core/proc` sentinels:
`UnsupportedPlatform` (bare), `SubreaperFailed` and `ReapFailed` (each an
`errs.Wrap` of the underlying syscall cause). Match with
`errs.HasCode(err, coreproc.CodeReapFailed)` etc.

## Tests

`reaper_internal_test.go` (white-box, `unix`): drain counting, Start/Stop
goroutine-leak check, idempotency, concurrent `ReapOnce` (race), and the
issue-#63 subreaper reparent-and-reap acceptance test (skipped with a typed
contract assertion when `PR_SET_CHILD_SUBREAPER` is unavailable).
