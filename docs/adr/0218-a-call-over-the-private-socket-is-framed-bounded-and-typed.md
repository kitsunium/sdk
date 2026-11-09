# ADR 0218 — a call over the private socket is framed, bounded and typed

- **Status**: Accepted
- **Date**: 2026-11-07
- **Deciders**: SDK maintainers (kitsunium/sdk#930, lot L4)
- **Related**: [ADR 0148](0148-a-private-socket-is-gated-by-its-directory-and-the-kernel-names-the-peer.md) (the socket a call rides), [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md) (the core at the engine's path, the codes there), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (the two frozen ports), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (no bound has a default), [ADR 0165](0165-a-performance-contract-is-the-designs-kit-measures-what-the-compiler-decides-and-a-test-holds-the-rest.md) (what a call costs, measured), [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md) (the design first)

## Context

`proc/ipc` hands out a `net.Conn` whose peer the kernel named, and nothing
above it. Every consumer wrote its own framing: the telemetry exporter a
newline-JSON handshake then fixed records, the framework's listeners theirs,
the statusline daemon its own. None had a request id, a deadline, a
cancellation, a bound on a frame or on the calls in flight. An admitted peer —
the same account, but a buggy or compromised process of it — could announce a
frame of 4 GiB and have the reader allocate it, hold a connection on half a
frame for ever, or open calls until the server ran out of goroutines. A product
wanting a typed call between two of its processes rebuilt the protocol, and
its holes, each time.

The standard library's `net/rpc` was measured against the need and set aside:
its codec is gob or a JSON-RPC 1.0 stream, a call has no deadline and no
cancellation on the wire, a request's size is bounded by nothing but the
decoder, and its server runs a goroutine per request with no ceiling.

## Decision

Three packages, designed in `design/proc/ipc/rpc.yaml`:

- `internal/core/proc/ipc/rpc` — the contract: the ports `Caller` (frozen at
  `Call`, `Stream`, `Ping`, `Close` — what a client holds and a test doubles),
  `Handler` and `Router` (each frozen at one method, ADR 0039), `Request` and
  `Stream`, and the codes of the new range `0.3.106.*` with their sentinels
  (ADR 0160).
- `internal/service/proc/ipc/rpc` — the engine: `Server` (`Serve` over a
  `proc/ipc` `Listener` port, `ServeConn` over one `Conn`) and `Client` (over
  the `Dialer` port: `Call`, `Stream`, `Ping` — the engine behind `Caller`),
  `HandlerFunc`, `Methods`. `ServeConn` returns once its handlers have, and
  `Serve` once its connections have: nothing it started outlives it.
- `pkg/v1/proc/ipc/rpc` — the facade, written by kit.

It holds only ports of `proc/ipc`, so it runs on the Unix socket and on the
Windows named pipe alike, and a test hands it `net.Pipe` doubles.

### 1. The wire

A client opens with a **hello**: the body format by the name a codec is
registered under in `core/data/codec`, the protocol version (1), and the
largest frame it reads. The server answers with its own hello, or with
`FORMAT_REFUSED` and closes. Each end then refuses to SEND a frame longer than
the other said it reads: `FRAME_TOO_LARGE` comes back to the caller, nothing is
written, and the connection stays.

Every frame is, big-endian:

```
len:u32 | kind:u8 | id:u64 | mlen:u8 | method[mlen] | body
```

`len` counts what follows it. The kinds: `hello` 1, `call` 2, `stream` 3,
`reply` 4, `item` 5, `end` 6, `error` 7, `cancel` 8, `ping` 9, `pong` 10. A
call's and a stream's body opens with the **budget**, `u64` nanoseconds left
on the caller's deadline (zero: none), then the encoded value. An error's body
is `code:u32 | rlen:u8 | reason | public` — an SDK error's code, reason and
public text, never its Private half nor its fields; an error with no SDK code
crosses as `REMOTE_FAILED`, its text kept on the server. The caller rebuilds it
with `errs.NewRuntime`, so `errs.HasCode` answers on both sides. Id 0 speaks for
the connection: the hello, and the error a server sends before it closes.

### 2. What a peer cannot do

- **Allocate on a length.** `MaxFrameBytes` is compared with the length before
  a byte of the body is read or allocated; past it the connection closes with
  `FRAME_TOO_LARGE`. A 4 GiB length is refused within that one frame, the
  process allocating under 256 KiB while it does (the test reads
  `runtime.MemStats`' `TotalAlloc` around it).
- **Hold half a frame.** The reader waits for a frame's first byte as long as
  the connection is idle — an idle connection is never closed — then the frame
  must be whole within `FrameTimeout`; a write as well. Past it the connection
  closes with `PROTOCOL_VIOLATION`.
- **Open calls without end.** At most `MaxInFlight` handlers run per
  connection; the next call is answered `OVERLOADED` at once, never queued. A
  server connection is `MaxInFlight + 2` goroutines: the reader, one
  **keeper** — a single goroutine and a single timer on the injected clock for
  every deadline of the connection, the frame read, the frame written and each
  call's budget — and the handlers.
- **Reuse an id, answer a call never made, send a kind out of place.**
  `PROTOCOL_VIOLATION`, and the connection closes.

### 3. Deadlines and cancellation

The client waits for its context and for `CallTimeout` on its injected clock,
whichever ends first, and on giving up sends a `cancel` frame for the id:
`CALL_TIMEOUT` or `CALL_CANCELLED`. The server turns the budget into a
deadline on ITS clock; the handler's context ends on the budget, on the cancel
frame, or with the connection, and `context.Cause` says which. A call's slot is
freed when its handler returns. Its answer and the release happen under the
connection's write lock, which admission also takes, so a client that waits
for each answer before its next call never finds its one slot taken.

### 4. Every bound is the caller's

`MaxFrameBytes` (64 to `MaxUint32`), `MaxInFlight`, `FrameTimeout` and the
client's `CallTimeout` have no default: zero is `MISCONFIGURED` (ADR 0031).
Only the clock has one, the wall clock.

## Consequences

- The socket's consumers can move onto one protocol; none is moved by this
  change (the telemetry exporter's records keep their wire until a decision on
  their format).
- A stream's items wait 16 deep for their reader, then the connection's reader
  waits too: a slow stream holds the connection's other calls behind it, and a
  long one wants a client of its own. A stream's reader that abandons it
  releases the connection at once.
- A handler that ignores its context keeps its slot until it returns: the
  bound counts goroutines that run, not calls the server wishes were over.
- The wire is versioned by the hello; a change to it is a new version and an
  amendment here.
- What a call costs is measured beside a raw write of the same bytes
  (`BENCH.md` of the engine).

## Codes, range `0.3.106.*`

| Code | Reason |
|---|---|
| `0.3.106.1` | `MISCONFIGURED` |
| `0.3.106.2` | `FRAME_TOO_LARGE` |
| `0.3.106.3` | `CALL_TIMEOUT` |
| `0.3.106.4` | `CALL_CANCELLED` |
| `0.3.106.5` | `METHOD_UNKNOWN` |
| `0.3.106.6` | `OVERLOADED` |
| `0.3.106.7` | `PROTOCOL_VIOLATION` |
| `0.3.106.8` | `FORMAT_REFUSED` |
| `0.3.106.9` | `CONN_CLOSED` |
| `0.3.106.10` | `REMOTE_FAILED` |
| `0.3.106.11` | `CODEC_FAILED` |
