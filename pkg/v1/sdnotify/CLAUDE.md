# pkg/v1/sdnotify/

## Purpose

Public, stable facade for the **sd_notify(3)** readiness/watchdog protocol. Thin
re-export of `internal/service/proc/sdnotify` over the `internal/core/proc`
contract — type aliases plus delegating functions, no new logic.

## Surface

```go
// notifier (child)
func Notify(state map[string]string) error
func Ready() error
func Reloading() error
func Stopping() error
func Status(msg string) error
func Watchdog() error
func MainPID(pid int) error

// supervisor + env
type Listener     = coreproc.Listener            // Recv / Close
type Notification = coreproc.NotificationValue    // State + Status/MainPID/SenderPID + Ready()/…
func Listen() (l Listener, socketPath string, err error)
func WatchdogInterval() (d time.Duration, ok bool)
```

## Why-this-shape

- **Aliases, never new types.** `Listener` and `Notification` are aliases of the
  `core/proc` types so a value produced by the service layer is identical to what
  consumers handle — no conversion, no parallel type hierarchy.
- **No-op when unsupervised.** The notifier funcs return `nil` and send nothing
  when `$NOTIFY_SOCKET` is unset; this is intentional libsystemd behaviour, not a
  swallowed error. A set-but-unreachable socket still returns `NotifyFailed`.
- **Listener is Linux-only.** It depends on `SO_PASSCRED` for the forge-proof
  `SenderPID`; on other platforms `Listen` returns `UNSUPPORTED_PLATFORM`. The
  notifier and `WatchdogInterval` are portable.

## README is generated

`README.md` is produced by `gomarkdoc` from the package doc comment in
`sdnotify.go` (ADR 0008). Edit the doc comment, then `make docs-readme` (or run
the `//go:generate` line at the top of `sdnotify.go`). Do not hand-edit
`README.md`.

## Do NOT

- Add behaviour here — all logic lives in `internal/service/proc/sdnotify`.
- Define error codes — sentinels are central in `internal/core/proc`
  (range `0.2.6.*`).
- Introduce new public types — alias the `core/proc` value types.

## Reference

- ADR 0016 — `docs/adr/0016-sdk-process-supervision-domain.md`
- `internal/core/proc/` — ports, value types, sentinels
