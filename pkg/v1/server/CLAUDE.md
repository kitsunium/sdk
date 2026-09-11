<!-- updated: 2026-09-09T00:00:00Z -->
# pkg/v1/server/

## Purpose

Public façade for the inbound half of the network domain (ADR 0029). Aliases
onto `internal/core/net` and `internal/service/net/server` plus thin forwarding
constructors. No logic lives here.

## The acceptance test

`TestTheShortestUsefulServer` is the API's own criterion. The requirement was to
plug a handler in *very easily*, which is not measurable by reading godoc — so
the test body is the complete, unabridged code for a working TCP server, and it
is five statements. If a change makes that example grow, the API has regressed
in the dimension that motivated the domain, whatever else it gained.

## Surface

| Symbol | Role |
|---|---|
| `Server`, `New` | the engine and its constructor |
| `Group` | listeners sharing one handler, chain and policy |
| `Conn` | one accepted connection; **embeds `net.Conn`** |
| `Handler`, `HandlerFunc`, `Middleware`, `Chain` | the handler shape |
| `State`, `ListenerState`, `Phase`, `Phase*` | lifecycle reporting |
| `Listen`, `TLS`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, `ReadBufferSize` | group options |
| `WithDrainTimeout` | server option |
| `DrainSignal` | the shutdown signal a handler holding a connection open watches |
| `ListenFailed` … `ConnLimitReached` | sentinels |

`sse/` is the Server-Sent Events half and `websocket/` is WebSocket (RFC 6455),
each in its own subpackage — see `pkg/v1/server/sse/CLAUDE.md` and
`pkg/v1/server/websocket/CLAUDE.md`. Both also record why there is no **client**
for their protocol and what would have to change in
`internal/core/net/response.go` for one.

## Why-this-shape

- **`Conn` embeds `net.Conn`**, so `io.Copy(c, c)` is a working echo server and
  every io helper keeps working. Everything the domain adds is additive.
- **`Group` returns the group, not `(group, error)`.** Declaration mistakes are
  recorded and reported by `Start`, which keeps the wiring chain readable
  without swallowing anything. `TestSentinelsAreMatchableThroughTheFacade`
  pins that each one surfaces.
- **One `TLS` option covers TLS and mutual TLS**, because the identity carries
  `RequireClientCert`. The same `tlsid.Identity` serves `pkg/v1/client`, so the
  two ends cannot drift apart.
- **The TLS tests complete a real handshake** rather than asserting struct
  fields, and a companion test proves a plaintext client is *not* served by a
  TLS group — otherwise the option could be decorative and still pass.
- **`State` reports the address actually bound**, not the `:0` requested, and
  surfaces any fallback via `Degraded()`.
- **`DrainSignal` is a channel on the context, not a cancellation.** A drain
  waits for in-flight work to finish, which assumes it eventually does; a
  handler that holds a connection open indefinitely breaks that assumption and
  used to hold `Shutdown` for its entire budget before being severed. Cancelling
  the request context to warn it would tell EVERY handler to abandon the
  response the drain exists to let it finish, so the warning is additive
  instead. An absent signal is a nil channel — receiving from nil blocks
  forever, so a `select` that watches it needs no nil check.
- **A hijacked connection is the handler's, and the drain does not wait for it**
  (ADR 0047 §D9). `websocket/` and anything else that takes the socket over stop
  being closed by the engine — a defect that predated WebSocket had the engine
  severing every hijacked socket the instant `net/http` reported it, so no
  upgrade could work at all. The carve-out matches `net/http`'s own; the drain
  signal is what reaches such a connection instead of the budget.

## Do NOT

- Retain a `Conn` or its `Buffer()` past `ServeConn` — both are recycled.
- Cancel a request context to signal a shutdown. Publish/observe `DrainSignal`.
- Hand-edit `README.md` — regenerate with `make docs-readme`.
- Add logic here; it belongs in `internal/service/net/server`.

## Verification

```
bazel test --config=race //pkg/v1/server:server_test
# Fallback:
cd pkg && GOWORK=off go test -race -cover ./v1/server/...
# expected: ~59% for the facade itself, ~83% for sse/.
#
# The facade's figure is not a gap. Seven forwarders here (HandshakeTimeout,
# MaxPacketSize, BatchSize, ChainPacket, MaxConns, Shards, Adopt) are one-line
# passthroughs whose behaviour is pinned in
# //internal/service/net/server:server_test, where the option can actually be
# observed; exercising them again through this package would assert that Go
# calls the function it was told to. Everything with behaviour of its own —
# New, Chain, DrainSignal, the sentinels — is covered here.
```

## Reference

- ADR 0029 — `docs/adr/0029-sdk-net-domain.md`
- ADR 0047 — WebSocket — `docs/adr/0047-sdk-net-websocket.md`
- `internal/core/net/CLAUDE.md`, `internal/service/net/server/CLAUDE.md`
