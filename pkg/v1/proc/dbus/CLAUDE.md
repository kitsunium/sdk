<!-- updated: 2026-11-08T14:00:00Z -->
# pkg/v1/proc/dbus/

## Purpose

Public facade over `internal/service/proc/dbus` (kitsunium/sdk#737, ADR
0214's S0e amendment): the SDK's message bus client on the standard library —
who owns a well-known name on a D-Bus daemon and every change of it, what
`pkg/v1/proc/service`'s `Type=dbus` is up on (`Config.BusWatcher`), and a
name a Go service owns. Its codes are `internal/core/proc/dbus`'s. Everything
here is written by `kit gen` from `design/proc/dbus.yaml` (`gen_facade.go`);
the README is `make docs-readme`'s.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Conn` | type alias | one connection, safe for concurrent use: `UniqueName`, `NameOwner`, `WatchName` (a `proc/service` `BusWatcher` as it is), `RequestName` (never queued), `ReleaseName`, `Close` |
| `NewConn` | func | forwarder: a unix socket address, AUTH EXTERNAL, Hello; `UNSUPPORTED_PLATFORM` off Linux |
| `SystemBusAddress`, `SessionBusAddress` | func | forwarders: the environment's address, else the well-known socket; never a launched bus |
| `Code*` | const | the nine codes, `0.3.108.*`, for `errs.HasCode` |

## Do NOT

- Write a forwarder or an alias by hand: the design declares them, `kit gen`
  writes them.
