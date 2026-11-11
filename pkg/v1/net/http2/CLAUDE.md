<!-- updated: 2026-11-10T00:00:00Z -->
# pkg/v1/net/http2/

## Purpose

Public facade of the SDK's HTTP/2 on the loop engine (ADR 0220, #961):
`HandleLoop`, the adapter a loop group serves, one chain for both HTTP/2 and
HTTP/1.1. Aliases and forwarders onto `internal/service/net/http2`; no logic
lives here.

## Surface

| Symbol | Role |
|---|---|
| `HandleLoop(h, p)` → `*LoopHandler` | `g.HandleLoop(http2.HandleLoop(h, p))`; the first bytes of a connection choose its version |

## Reference

ADR 0220, ADR 0186, `internal/service/net/http2/CLAUDE.md`.
