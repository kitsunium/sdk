<!-- updated: 2026-09-03T00:00:00Z -->
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
| `ListenFailed` … `ConnLimitReached` | sentinels |

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

## Do NOT

- Retain a `Conn` or its `Buffer()` past `ServeConn` — both are recycled.
- Hand-edit `README.md` — regenerate with `make docs-readme`.
- Add logic here; it belongs in `internal/service/net/server`.

## Verification

```
bazel test --config=race //pkg/v1/server:server_test
# Fallback:
cd pkg && GOWORK=off go test -race -cover ./v1/server/...
# expected: coverage 100%
```

## Reference

- ADR 0029 — `docs/adr/0029-sdk-net-domain.md`
- `internal/core/net/CLAUDE.md`, `internal/service/net/server/CLAUDE.md`
