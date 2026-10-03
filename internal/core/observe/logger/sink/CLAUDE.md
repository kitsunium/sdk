<!-- updated: 2026-10-03T12:00:00Z -->
# internal/core/observe/logger/sink/

## Purpose

A directory, not a package: it holds no Go code and nothing imports it. Each
member holds the codes and sentinels of one of the logger engine's terminal
sinks, `internal/service/observe/logger/sink/<name>`, at the same path — ADR
0160 §2 puts every code of a service package in the core package that mirrors
it, and the engine declares none. A terminal has no port of its own: it is a
`Sink` (`internal/core/observe/logger`).

## Members

| Package | Engine | Code range |
|---|---|---|
| `console/` | an `io.Writer` under a mutex | `0.3.13.*` |
| `file/` | an append-only, symlink-refusing `*os.File` | `0.3.14.*` |
| `syslog/` | RFC 5424 over UDP or TCP | `0.3.15.*` |

`sink/memory` declares no code, so it has no mirror. Each range is the one
ADR 0005 allocated to the engine; its value did not change when its
declaration moved here (ADR 0160 §3).

## Do NOT

- Put Go code in this directory.
- Put behaviour in a member: what a sink does is its engine's.
