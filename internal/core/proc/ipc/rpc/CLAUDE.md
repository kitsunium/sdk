<!-- updated: 2026-11-07T00:00:00Z -->
# internal/core/proc/ipc/rpc/

## Purpose

The **contract** of a call between two processes over the private socket
(ADR 0218, #930): the `Handler` that answers one method, the `Router` that
names it, the `Request` a handler reads, the `Stream` a caller reads a
server's items from, the `Caller` a client is, and the codes every refusal carries. The engine — frames
bounded before they are read, `MaxInFlight` calls per connection, deadlines on
an injected clock, a cancel frame — is `internal/service/proc/ipc/rpc`, at the
mirrored path (ADR 0160). Nothing here opens a socket or reads a frame.

Range `0.3.106.*` (reserved for #930, `design/sdk.yaml` `codes.ranges`).
Imports: `context`, `internal/core/proc/ipc` (for `PeerValue`) and
`internal/kernel/errs`.

**Generated from the design** (ADR 0163): the five ports in `gen_ports.go`,
the codes and sentinels in `gen_codes.go`, the pins in `gen_api_test.go`, all
from `design/proc/ipc/rpc.yaml`. Change the design, then `kit gen`, then
`make api` — never the generated files.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Caller` | port, frozen at four | `Call(ctx, method, in, out)`, `Stream(ctx, method, in)`, `Ping(ctx)`, `Close()` — what a client holds, and a test doubles; the engine's `*Client` is one |
| `Handler` | port, frozen at one | `ServeRPC(ctx, req) (out, err)` — a Call's reply is `out`; a Stream's items go through `req.Send`, and a non-nil `out` is one last item |
| `Router` | port, frozen at one | `Route(method) (Handler, bool)`; false is `METHOD_UNKNOWN` for the caller |
| `Request` | port | `Method`, `Peer` (the kernel's word, `coreipc.PeerValue`), `Decode(v)`, `Send(item)` |
| `Stream` | port | `Recv(v) (more, err)` — false and nil at the end — and `Close` (sends the cancel frame) |

## Error codes (range `0.3.106.*`)

| Code | Sentinel | Reason |
|---|---|---|
| `0.3.106.1` | `Misconfigured` | `MISCONFIGURED` — a bound not positive, a format no codec is registered under, a method out of 1..255 bytes; exit 78 |
| `0.3.106.2` | `FrameTooLarge` | `FRAME_TOO_LARGE` — a length past `MaxFrameBytes`, refused before the body is read or allocated; the connection closes (on the sending side: nothing written, the connection stays) |
| `0.3.106.3` | `CallTimeout` | `CALL_TIMEOUT` — the caller's deadline or `CallTimeout`; the server's budget on its own clock |
| `0.3.106.4` | `CallCancelled` | `CALL_CANCELLED` — the caller cancelled; a cancel frame was sent |
| `0.3.106.5` | `MethodUnknown` | `METHOD_UNKNOWN` — the Router said false |
| `0.3.106.6` | `Overloaded` | `OVERLOADED` — `MaxInFlight` already running, answered at once; exit 75 |
| `0.3.106.7` | `ProtocolViolation` | `PROTOCOL_VIOLATION` — a frame out of place, short, reused, or not whole within `FrameTimeout`; exit 76 |
| `0.3.106.8` | `FormatRefused` | `FORMAT_REFUSED` — a hello's format or version the server does not speak; exit 76 |
| `0.3.106.9` | `ConnClosed` | `CONN_CLOSED` — a closed client, or a connection that ended with the call in flight; exit 69 |
| `0.3.106.10` | `RemoteFailed` | `REMOTE_FAILED` — a handler error with no SDK code; its text stays on the server |
| `0.3.106.11` | `CodecFailed` | `CODEC_FAILED` — the format refused a value or a body; exit 65 |

No Public and no Private quotes a body: a call's values may be anything.

## Do NOT

- Add a method to a port: `Caller`, `Handler` and `Router` are frozen (ADR 0039); a capability that comes later is a sibling interface.
- Change a code's value: a caller branches on it, on both ends of the socket.
- Put the wire format here: it is the engine's, and ADR 0218's.
