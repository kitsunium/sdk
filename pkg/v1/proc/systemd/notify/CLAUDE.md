# pkg/v1/proc/systemd/notify/

## Purpose

Public, stable facade for the **sd_notify(3)** readiness/watchdog protocol. Thin
re-export of `internal/service/proc/systemd/notify` over the `internal/core/proc`
contract — type aliases plus delegating functions, no new logic.

The package is `notify`. It was `sdnotify`, at the root of `pkg/v1`, until
ADR 0155 put the proc facades under `pkg/v1/proc` and the two systemd protocols
under `systemd/`: the import path's last element and the package name agree,
so a caller writes `notify.Ready()`. The package comment is in `doc.go`, which
kit writes from the design (ADR 0167).

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

`README.md` is written by `tools/genindex` from the committed `docs/api`
(`make docs-readme`, ADR 0167); do not hand-edit it. The package comment is in
`doc.go`, which kit writes from the design (`design/proc.yaml`): edit the
design, run `kit gen`, then `make api` and `make docs-readme`.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/proc.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the declarations of their own, and `doc.go` — kit's too (ADR 0167) — the package comment. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Do NOT

- Add behaviour here — all logic lives in `internal/service/proc/systemd/notify`.
- Define error codes — sentinels are central in `internal/core/proc`
  (range `0.2.6.*`).
- Introduce new public types — alias the `core/proc` value types.

## Reference

- ADR 0016 — `docs/adr/0016-sdk-process-supervision-domain.md`
- `internal/core/proc/` — ports, value types, sentinels
