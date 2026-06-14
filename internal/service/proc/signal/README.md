# signal (service)

Typed signal toolbox backing `pkg/v1/signal`. Subscribe to OS signals on a
leak-free typed channel (`Notify`) and forward received signals to a process or
process group (`Relay`).

```go
import (
    coreproc "github.com/kitsunium/sdk/internal/core/proc"
    svcsignal "github.com/kitsunium/sdk/internal/service/proc/signal"
)

// Subscribe and react to a graceful-shutdown request.
ch, stop := svcsignal.Notify(coreproc.Signal(syscall.SIGTERM))
defer stop()
<-ch

// Forward every received signal to a child process group (negative target).
src, stop := svcsignal.Notify(coreproc.Signal(syscall.SIGTERM))
defer stop()
go svcsignal.Relay(src, svcsignal.Target(-childPGID))
```

## Surface

- `type Target int` — a positive value is a pid; a value below -1 is `-pgid`
  (kill(2) delivers to the whole group whose id is its absolute value).
- `func Notify(sigs ...coreproc.Signal) (<-chan coreproc.Signal, func())` —
  returns a buffered typed channel plus an idempotent, leak-free stop func.
- `func Relay(src <-chan coreproc.Signal, target Target) error` — forwards each
  signal via kill(2) until `src` closes; `nil` on clean drain, `RELAY_FAILED` on
  the first delivery failure, `UNSUPPORTED_PLATFORM` off Unix.

## Notes

- `Notify` is portable (`os/signal`); the kill(2) path is build-tag split into
  `relay_unix.go` / `relay_other.go`.
- `Parse` / `String` are owned by `core/proc.Signal`; this package does not
  re-implement them.
- No error codes are defined here — `Relay` restates the central
  `core/proc.RelayFailed` sentinel when wrapping the kill(2) cause.

See `CLAUDE.md` for lifecycle, leak-freedom, and platform rationale.
