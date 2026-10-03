<!-- updated: 2026-10-03T04:40:00Z -->
# internal/service/net/

## Purpose

The net family's engines (ADR 0155): the concrete halves of the network
domain's one contract, `internal/core/net` (ADR 0029). This directory holds no
Go code: it is a prefix, not a package, and nothing imports
`internal/service/net` itself. Each member is a package of the
`internal/service` module with its own `CLAUDE.md`, and each is published by the
facade at the same path under `pkg/v1/net`.

## The rule that put them together

A domain belongs here when it moves bytes between processes over a socket: the
inbound engine (`server`) and the outbound guarded transport (`client`), the
reading of the TLS identity both present (`tlsid`), and the three things served
over the inbound engine's HTTP — Server-Sent Events (`sse`), WebSocket
(`websocket`, ADR 0047) and a file tree (`static`, ADR 0130). They implement ONE
contract: `internal/core/net` holds the ports, the values and every `0.2.11.*`
sentinel, and these engines wrap those sentinels and declare no code of their
own (the `proc` shape, ADR 0016). Reading and writing a wire format is the
engines' own (ADR 0160 §4): the WebSocket frame codec, close payload,
handshake digest and UTF-8 check are `websocket`'s, the SSE encoder is
`sse`'s; the core keeps the values they read and write. A private socket between processes of one
machine is the proc family's (`ipc`, ADR 0148).

## Members

| Package | What it is | Implements | Code range | Facade |
|---|---|---|---|---|
| `server/` | one listener engine for TCP, Unix, TLS and mutual TLS, handler groups behind shared middlewares, a bounded drain announced to the handler (ADR 0029, ADR 0043), a failing accept that waits on `kernel/backoff` (ADR 0130); adopts a passed socket through `internal/service/proc/systemd/listen` | `core/net` | none (core `0.2.11.*`) | `pkg/v1/net/server` |
| `client/` | the policy-enforcing HTTP transport: the guarded `RoundTripper`, per-phase timeouts, the response size cap, the concrete policies and the `Client` (ADR 0029) | `core/net` | none (core `0.2.11.*`) | `pkg/v1/net/client` |
| `tlsid/` | reads PEM material from disk and nothing else; every validation rule is `core/net.NewIdentityValue`'s (ADR 0029) | `core/net` | none (core `0.2.11.*`) | `pkg/v1/net/tlsid` |
| `sse/` | an HTTP response held open and written one `text/event-stream` frame at a time, on `net/http`'s own interfaces, and the frame's wire form (`AppendEvent`, `AppendComment` — ADR 0029, ADR 0043, ADR 0160) | `core/net` | none (core `0.2.11.*`) | `pkg/v1/net/sse` |
| `websocket/` | RFC 6455 in the stdlib — the handshake, the frame codec, the close payload and the UTF-8 check included — every MUST-fail enforced, the origin checked by default (ADR 0047, ADR 0069, ADR 0160) | `core/net` | none (core `0.2.11.*`) | `pkg/v1/net/websocket` |
| `static/` | an `http.Handler` over an `io/fs.FS` that never lists a directory and sends the security headers on every response (ADR 0130) | `core/net` | none (core sentinel `0.2.11.34`) | `pkg/v1/net/static` |

No member imports another: each composes `internal/core/net` and the kernel,
and `server` alone reaches another family — `proc`, for socket activation
(`internal/core/proc`, `internal/service/proc/systemd/listen`).
None of them is in `//:audit_sources`: none declares a code, so there is
nothing for the errs audits to judge (`check-audit-coverage.sh` requires
exactly the declaring packages), and `codeRangeOwners` gives `0x00_02_0B_00`
to `internal/core/net` alone.

## Do NOT

- Put Go code in this directory. A file here would make `net` a package of its
  own beside the contract that already carries the name.
- Declare an error code in a member. Add the sentinel to `internal/core/net` and
  wrap it here (`internal/core/net/CLAUDE.md`).
- Import one member from another without saying so in both `CLAUDE.md` files:
  today none does.
