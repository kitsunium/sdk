<!-- updated: 2026-09-28T19:19:15Z -->
# internal/service/proc/signal

Service implementation of the typed **signal toolbox** for the process-supervision
domain (ADR 0016). It backs the `pkg/v1/proc/signal` facade and depends only on
stdlib + `internal/core/proc` + `internal/kernel/errs` (never `pkg/*`).

## What lives here

| Symbol | File | Responsibility |
|---|---|---|
| `Target` | `signal.go` | recipient of a relayed signal — positive pid, or `< -1` for `-pgid` |
| `Notify` | `signal.go` | subscribe to a set of signals on a leak-free typed channel |
| `Relay` | `relay_unix.go` / `relay_windows.go` / `relay_other.go` | forward received signals to a pid / process group — kill(2) on Unix, the kernel32 primitives on Windows |

`Parse` / `String` are NOT re-implemented here — they belong to `core/proc.Signal`
and are re-exported only by the public facade.

## Notify — lifecycle & leak-freedom

`Notify` registers an `os/signal` channel and starts ONE translation goroutine.
The goroutine owns the whole registration lifecycle: it calls `signal.Notify`,
`defer signal.Stop`, signals readiness on a `ready` channel (so `Notify` only
returns once a signal can no longer be missed — no registration race), then
translates each `os.Signal` carrier to a typed `coreproc.Signal` until `done` is
closed.

The returned `stop`:
- closes `done` → the goroutine returns → its defer runs `signal.Stop` and closes
  the output channel;
- is **idempotent** (guarded by a `sync.Once`, so concurrent calls cannot
  double-close `done`) — a second call is a no-op;
- is **leak-free** — the goroutine's deferred `signal.Stop` and the close of the
  output channel run on that goroutine just after `stop` returns (see the
  caveat under Performance), so a reader sees the teardown as the channel
  closing; the external test asserts that close rather than a
  `runtime.NumGoroutine` count.

The output channel is buffered (`max(len(sigs), 1)` slots) so a burst is not
dropped while the reader is busy, honouring the os/signal "never blocks the
sender" contract.

## Relay — kill(2) semantics

`Relay` ranges over the source channel and delivers each signal with
`syscall.Kill(int(target), sig)`:
- `target > 0` → a single pid;
- `target < -1` → the process group whose id is `-target` (the kill(2)
  negative-pid convention, see kill(2)).

The reserved targets `0` (the caller's own process group) and `-1` (every
process the caller may signal) are refused with `RelayFailed` before anything
is delivered, so a zero-value `Target` never fans out. A clean drain of the
source returns `nil`; the first delivery failure stops the
relay and returns `coreproc.RelayFailed` (code `RELAY_FAILED`, exit 71) wrapping
the kill(2) cause, with `target` and `signal` fields attached. A value whose
carrier is not a `syscall.Signal` is skipped rather than aborting the relay.

## Platform split

`os/signal` is portable, so `Notify` is cross-platform and lives in `signal.go`.
Only the delivery path is split:
- `relay_unix.go` (`//go:build unix`) — real kill(2) delivery;
- `relay_windows.go` (`//go:build windows`) — kernel32 bound through
  `syscall.NewLazyDLL`: a group target gets `GenerateConsoleCtrlEvent`
  (`CTRL_C` for SIGINT, `CTRL_BREAK` otherwise), a pid gets `OpenProcess` +
  `TerminateProcess`; the reserved targets are refused as on Unix;
- `relay_other.go` (`//go:build !unix && !windows`) — returns
  `coreproc.UnsupportedPlatform`, so every GOOS compiles and merely degrades.

## Performance — see `BENCH.md`

**`Notify` is a wiring-time call, not a per-request one.** Registration costs
**≈7 300 ns**; the per-delivery translation it buys costs **2.98 ns** — a ratio of
**≈2 460×**. Subscribing to four signals in ONE call (13.4 µs) is 2.2× cheaper
than four separate calls (29.3 µs) and spawns one goroutine instead of four, at
the same allocation count.

`Relay` costs **≈346 ns per delivered signal**, which is a bare `kill(2)` on this
kernel — the loop adds nothing measurable per signal, and ~196 ns once per `Relay`
call. `stop()` after the first is 3.1 ns (the `sync.Once` fast path), so a
defensive teardown path pays nothing.

One caveat on reading the registration figure: `stop()` closes `done` and returns;
the goroutine's own `signal.Stop` and `close(dst)` run just afterwards on that
goroutine, so **deregistration is not inside the ≈7 300 ns**.

## Errors

No codes are defined here. All sentinels are central in `internal/core/proc`;
`Relay` wraps its kill(2) cause by **restating** the `RELAY_FAILED` sentinel
fields verbatim (never `errs.Define`). Match with
`errs.HasCode(err, coreproc.CodeRelayFailed)`.

## Tests

The #62 acceptance criteria are covered by two `//go:build unix` files:
- `signal_external_test.go`: `Notify` delivers a self-sent `SIGUSR1`,
  `SIGWINCH` or burst of `SIGUSR2`, then `stop` closes the channel (the proof
  the goroutine returned) and a second `stop` is a no-op;
  `TestNotifyStopConcurrent` calls `stop` from many goroutines at once, which
  `-race` fails if the `sync.Once` guard regresses;
- `relay_unix_external_test.go`: `Relay` to a **negative** target delivers
  SIGTERM to a child forked into its OWN process group (`Setpgid`) — isolated so
  the group kill never reaches the test runner's group — and the child is
  observed to die from exactly SIGTERM; the reserved targets `0` and `-1`, and a
  pid that cannot exist, surface `CodeRelayFailed`.

`relay_windows_test.go` (`//go:build windows`) pins the reserved-target
refusal, a pid terminated, and a clean drain; `relay_other_test.go`
(`//go:build !unix && !windows`) asserts the stub returns
`CodeUnsupportedPlatform`.
