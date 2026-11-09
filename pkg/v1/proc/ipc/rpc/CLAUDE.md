<!-- updated: 2026-11-07T00:00:00Z -->
# pkg/v1/proc/ipc/rpc/

## Purpose

Public facade over `internal/service/proc/ipc/rpc` (ADR 0218, #930): a call
between two processes of one machine over `proc/ipc`'s private socket — a
request and its reply, or a request and the items a server pushes — framed,
bounded and encoded in a codec format both ends agreed on. Its contract — the
ports and the codes — is `internal/core/proc/ipc/rpc`, and the aliases point
there (ADR 0074). Everything here is written by `kit gen` from
`design/proc/ipc/rpc.yaml` (`gen_facade.go`); the README is `make docs-readme`'s.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Caller`, `Handler`, `Router`, `Request`, `Stream` | type alias, port | the core's ports; a `*Client` is a `Caller`, so code that calls holds the port and a test hands it a double |
| `ServerConfig`, `ClientConfig` | type alias | the engine's bounds: `MaxFrameBytes`, `MaxInFlight`, `FrameTimeout` (and the client's `CallTimeout`), none with a default — zero is `MISCONFIGURED` (ADR 0031); `Clock` nil is the wall clock |
| `Server`, `Client` | type alias | the engines: `Serve(ln)`, `ServeConn(ctx, c)`, `InFlight`, `Close`; `Call`, `Stream`, `Ping`, `Close` |
| `HandlerFunc`, `Methods` | type alias | a function as a `Handler`, a fixed table as a `Router` |
| `NewServer`, `NewClient` | func | forwarders |
| `Code*` | const | the eleven codes, `0.3.106.*`, for `errs.HasCode` |

A format is a codec registered under its name: the caller imports the codec's
package (`pkg/v1/data/codec/json`, say). `NewClient` takes the `proc/ipc`
`Dialer` port, `Serve` its `Listener` port, so a test hands both doubles.

## Do NOT

- Write a forwarder or an alias by hand: the design declares them, `kit gen` writes them.
- Give a bound a default: every one is the caller's decision (ADR 0031).
