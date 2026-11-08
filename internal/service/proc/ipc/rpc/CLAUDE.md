<!-- updated: 2026-11-07T00:00:00Z -->
# internal/service/proc/ipc/rpc/

## Purpose

A call between two processes of one machine over the private socket of
`proc/ipc` (ADR 0218, #930): a request and its reply (`Call`), a request and
the items a server pushes (`Stream`), a round trip that runs no handler
(`Ping`). Before it, every consumer of the socket — the telemetry exporter's
newline-JSON handshake, the framework's listeners, the statusline daemon —
wrote its own framing with no request id, no deadline, no cancellation, no
frame bound and no backpressure, and an admitted peer could hold a connection
on half a frame for ever. Public facade: `pkg/v1/proc/ipc/rpc`.

Stdlib only, plus the kernel's `clock` and `errs`, the codec registry of
`internal/core/data/codec` (a format is found by its name; nothing here
imports a codec) and the ports of `internal/core/proc/ipc` (`Listener`,
`Dialer`, `Conn`) — so it runs on the Unix socket and the Windows pipe alike,
and a test hands it `net.Pipe` doubles. Its contract is
`internal/core/proc/ipc/rpc` (`corerpc`): the `Handler`, `Router`, `Request`
and `Stream` ports, and the `0.3.106.*` codes and sentinels — this package
returns them and declares none.

## Contents

| File | Role |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `frame.go` | the wire (ADR 0218): the kinds (numbered from 1 in the wire's order), `wireFormat` — the connection's codec as a `codec.Appender`, the frame built in place through the codec's own `Append` when it has one —, `readFrame` — the first byte waits as long as the connection is idle, then FrameTimeout is armed, the length is checked against `MaxFrameBytes` BEFORE anything is allocated, then one allocation for the frame —, `beginFrame`/`finishFrame` (refuses a frame past the peer's hello bound before a byte is written), the hello, the error frame (code, reason and public text — never Private nor fields — or `REMOTE_FAILED`) and `remoteError` (rebuilt with `errs.NewRuntime`, so `errs.HasCode` answers on the caller's side) |
| `keeper.go` | the connection's timekeeper: ONE goroutine and ONE timer on the injected clock for the frame read, the frame written and every call's budget — why a server connection is `MaxInFlight + 2` goroutines |
| `config.go` | the bounds both ends share, each refused at zero (ADR 0031) — `MaxFrameBytes` between 64 and `MaxUint32` —, the format's lookup in the codec registry, the clock's default |
| `server.go` | `ServerConfig`, `Server`, `NewServer`, `Serve` (a goroutine per accepted connection, waited for; nil after `Close`), `ServeConn` (returns once its handlers have), `InFlight`, `Close` |
| `serverconn.go` | one served connection: the hello, the reader's dispatch, admission (`OVERLOADED` at once past `MaxInFlight`, a reused id a violation), the budget armed on the server's clock, a cancel frame ending the handler's context, `serverCall` — the `Request` — and its answer; a call's answer and its slot's release happen under the write lock, which admission also takes, so a client that waits for each answer never sees `OVERLOADED` on one slot |
| `client.go` | `ClientConfig`, `Client` — the engine behind the core `Caller` port —, `NewClient` (dial, hello within ctx and FrameTimeout), the reader that hands each answer to its call (an answer to an id never issued is a violation; one to an abandoned call is dropped), `Call`, `Ping`, `await` (ctx, `CallTimeout` on the clock, the connection's end — giving up sends the cancel frame), `Close` |
| `stream.go` | `Client.Stream` and `clientStream`: 16 frames buffered, then the connection's reader waits — on the reader or on the stream's abandonment, never for ever |
| `router.go` | `HandlerFunc`, `Methods` |
| `rpc_compliance.go` | the engines against the core's ports (`Caller`, `Handler`, `Router`, `Request`, `Stream`) and `wireFormat` against `codec.Appender` |
| `rpc_external_test.go`, `frames_external_test.go` | the acceptance of #930 — 1 000 calls bounded by `MaxInFlight` with the connection's goroutines counted, a cancelled call freeing its slot, a `CallTimeout` sending the cancel frame the handler sees, a 4 GiB length refused with `runtime.MemStats`' `TotalAlloc` under 256 KiB —, the budget on the server's clock, half a frame closed after `FrameTimeout`, the format refused, the bounds refused |
| `socket_unix_external_test.go` | a call over a real `proc/ipc` socket under `t.TempDir()` (skipped, after its refusal is checked, where the path exceeds `sun_path`) |
| `rpc_bench_test.go` | `BenchmarkCallRoundTrip` against `BenchmarkRawWriteRoundTrip`, numbers in `BENCH.md` |

## Invariants

- A frame's length is compared with `MaxFrameBytes` before its body is read or allocated, on both ends.
- A frame begun is whole within `FrameTimeout` on the injected clock, read or written, or the connection closes; an idle connection is never closed.
- A server connection runs at most `MaxInFlight` handler goroutines, plus its reader and its keeper.
- A handler's error reaches the caller only through its SDK code, reason and public text.
- Nothing has a default but the clock: zero bounds are refused.

## Do NOT

- Give a call a goroutine or a timer of its own on the server: the keeper holds every deadline.
- Read a body before its length was checked, or reuse a frame's buffer while its handler may still decode it.
- Answer a call after releasing its slot outside the write lock: a waiting client would then race its next call against the release.
- Change the wire without ADR 0218 and the protocol version.
