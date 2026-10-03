<!-- updated: 2026-10-03T12:00:00Z -->
# internal/core/observe/logger/middleware/

## Purpose

A directory, not a package: it holds no Go code and nothing imports it. Each
member holds the codes and sentinels of one of the logger engine's `Sink`
decorators, `internal/service/observe/logger/middleware/<name>`, at the same
path — ADR 0160 §2 puts every code of a service package in the core package
that mirrors it, and the engine declares none. A decorator has no port of its
own: it is a `Sink` (`internal/core/observe/logger`) wrapping another.

## Members

| Package | Engine | Code range |
|---|---|---|
| `multi/` | fan-out to every branch | `0.3.16.*` |
| `async/` | ring + drainer in front of a slow sink | `0.3.17.*` |
| `route/` | predicate dispatch | `0.3.18.*` |
| `failover/` | ordered retry across a chain | `0.3.19.*` |
| `sample/` | 1-of-N | `0.3.20.*` |
| `recover/` | a downstream panic as a typed error | `0.3.21.*` |
| `encwrite/` | each record sealed under a per-sink subkey | `0.3.28.*` |
| `tee/` | fan-out with a dead-letter spill | `0.3.29.*` |

Each range is the one ADR 0005 / ADR 0014 allocated to the engine; its value
did not change when its declaration moved here (ADR 0160 §3).

## Do NOT

- Put Go code in this directory.
- Put behaviour in a member: what a decorator does is its engine's.
