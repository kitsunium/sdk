<!-- updated: 2026-10-26T12:00:00Z -->
# pkg/v1/net/http1/

## Purpose

Public facade of the restricted HTTP/1.1 profile (ADR 0186 §D6, §D9 step 12):
the loop adapter `HandleLoop`, the handler ports, the request and response
values, the codec and its sentinels. Aliases onto `internal/core/net` and
`internal/service/net/http1`, plus forwarders. No logic lives here.

A package of its own beside `server`, as `sse`, `websocket` and `static` are:
`server` serves bytes, this package speaks the profile over them, and
`server.Group.HandleLoop(http1.HandleLoop(h, http1.Parser{}))` reads the way
the job is discussed.

## Surface

| Symbol | Role |
|---|---|
| `HandleLoop(h, p)` → `*LoopHandler` | the `server.LoopHandler` of a loop group serving `h`; `p` sets the limits, the zero `Parser` the defaults |
| `Handler`, `HandlerFunc` | what a consumer writes: `ServeHTTP1(*Request, *Response)` |
| `Batcher` | optional: `EndBatch(lane)` once per batch, before its answers leave — where a handler runs the work it batched and completes what it deferred |
| `Request`, `Response`, `Field`, `Limits` | the values; `Request.Lane` names the lane, `Response.Defer` answers at the batch's end |
| `Parser`, `NewParser`, `Continue`, `Pending`, `Refusal`, `RefusalNone` | the codec's framing, usable without the loop |
| `AppendStatusLine`, `AppendField`, `AppendResponse`, `AppendRefusal` | the codec's writer |
| `MaxFields`, `DefaultHead`, `MaxHead`, `DefaultTarget`, `DefaultBody`, `MaxBody` | the bounds |
| `RequestMalformed` … `Misconfigured` | the core's `HTTP1*` sentinels, `0.2.11.45` – `0.2.11.55`; a `Refusal`'s `Err` is one of them |

## Why-this-shape

- **Only `RefusalNone` of the 23 refusals is re-exported.** A consumer branches
  on the sentinel (`Refusal.Err`) and logs the `Refusal`'s `String`; the
  values themselves stay the codec's, so a refusal added later is not a
  surface change.
- **`HandleLoop` takes a `Parser`, not a `Limits`.** The limits are validated
  where they are built (`NewParser`), so `HandleLoop` cannot fail.
- `TestHandleLoopServesHTTP` serves a `Batcher` written with this package and
  `server` alone on every backend the platform has, through `net/http`'s
  client and a pipelined raw connection; `TestCodecThroughTheFacade` reaches
  the codec and the sentinels.

## Do NOT

- Add logic here; it belongs in `internal/service/net/http1`.
- Hand-edit `README.md` — regenerate with `make docs-readme`.
