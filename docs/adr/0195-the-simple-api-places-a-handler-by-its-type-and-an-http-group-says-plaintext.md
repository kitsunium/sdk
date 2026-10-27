# ADR 0195 — the simple API places a handler by its type, and an HTTP group says Plaintext

- **Status**: Accepted
- **Date**: 2026-10-27
- **Accepted**: 2026-10-27, carrying the entry point of step P2 of the unified
  network work: the owner's decisions D-N7 and D-N8 of 2026-10-24, which
  [ADR 0187](0187-the-handler-type-chooses-the-engine.md) §D6 and §D7 recorded
  and staged. The spelling of the default group name and the datagram
  refusal's code below were chosen by the lead maintainer while the owner was
  away, and are open to the owner's review. It can be revisited while the SDK
  is v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers, owner
- **Implements**: ADR 0187 §D6, §D7 and the datagram half of §D4

## Context

ADR 0187 decided that the handler's type chooses the engine, and named the
entry point that makes it so — `srv.HTTP`, `srv.TCP`, `srv.UDP` — with its typed
errors; it staged two refusals until that entry point existed, because a
consumer had no one-line way to comply: an HTTP group without TLS must say
`Plaintext()`, and a datagram group refuses every option it does not honour.

## Decision

### D1 — Three entries

`Server.HTTP(addr, h any, opts...)`, `Server.TCP(addr, h any, opts...)` and
`Server.UDP(addr, h any, opts...)` declare a group listening on `addr` and serve
`h` on the engine its type runs on:

| Entry | Goroutine engine | Loop engine |
|---|---|---|
| `HTTP` | `http.Handler`, through net/http | `HTTP1Handler`, through the http1 codec (`http1.HandleLoop`) |
| `TCP` | `ConnHandler` | `LoopHandler` |
| `UDP` | `PacketHandler` | — |

They are built on `Group` and `PacketGroup`; `opts` apply exactly as they do
there. A group is named `network+"+"+addr` — `tcp+:8080`, `udp+:53` — unless
`Name` names it; the default name is the declaration's, so `Name` replaces it
rather than conflicting with it.

### D2 — The typed errors, all reported by `Start`

| Handler | Error |
|---|---|
| nil, or a typed nil (a nil pointer, func, map …) | `HandlerMissing`, field `entry` |
| both interfaces of the entry, no `Engine` | `HandlerAmbiguous` (`0.2.11.64`) |
| an `Engine` the handler cannot run on | `EngineUnsupported` |
| a type no engine of the entry serves — a `PacketHandler` given to `HTTP`, a `LoopHandler` to `UDP` | `HandlerUnsupported` (`0.2.11.65`), fields `entry`, `type` |
| an HTTP handler given to `TCP` | `HandlerUnsupported`, field `serve` = `HTTP` |
| `Plaintext()` with `TLS(id)` | `PlaintextWithTLS` |

### D3 — HTTP without TLS says `Plaintext()` (D-N8)

A group that serves HTTP — net/http's adapter, or the http1 codec as its loop
handler — given neither `TLS` nor `Plaintext()` fails `Start` with
`PlaintextRequired` (`0.2.11.66`), whatever entry point declared it:
`HandleHTTP` and `HandleLoop(http1.HandleLoop(…))` included. ADR 0187 §D7's
staging ends here.

### D4 — A datagram group refuses what it does not honour

A datagram group — `UDP` or `PacketGroup` — applies `MaxPacketSize`,
`BatchSize`, `Adopt`, `Name`, `Engine` (the goroutine engine only) and
`Plaintext`. Every other option, `TLS` and every timeout included, fails
`Start` with `PacketOptionUnsupported` (`0.2.11.67`), naming it; it was
dropped in silence before (R6). It is a code of its own, not
`LoopOptionUnsupported`, because no loop is involved.

## Breaking changes

Allowed while v0 (ADR 0040), announced by ADR 0187:

1. An HTTP group with neither `TLS` nor `Plaintext()` fails `Start`. Add
   `Plaintext()`; the framework's HTTP engine does.
2. A datagram group given an option it does not honour fails `Start`. Drop the
   option.

## Consequences

- New: `Server.HTTP`, `Server.TCP`, `Server.UDP`; `HandlerAmbiguous`,
  `HandlerUnsupported`, `PlaintextRequired`, `PacketOptionUnsupported`
  (`0.2.11.64` – `0.2.11.67`), re-exported by `pkg/v1/net/server`.
- `Group`, `PacketGroup` and every `Handle*` stay: the simple API is built on
  them, and a program that needs two handlers kinds on one address shape, or
  an adopted socket alone, still declares a group.
- An `HTTP1Handler` with `TLS` is still refused (`LoopTLSUnsupported`) until
  ADR 0187 §D5's portable path lands.

## Alternatives considered

- **Resolve the handler only at `Start`.** Equivalent, since every option is
  applied when the entry returns; resolving at once lets `UseLoop` and `Use`
  be chained on the group it returns.
- **Name the default group `network:addr`.** Rejected: `tcp::8080` for `:8080`
  reads as a typo; `+` cannot occur in a network name or an address.
