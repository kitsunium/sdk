<!-- updated: 2026-10-26T12:00:00Z -->
# internal/service/net/http1/

The reference half of this file moved, unchanged and under the same section titles, to
`internal/service/net/http1/NOTES.md` (ADR 0253): read the section a change touches.
It holds:
§Contents; §Why-this-shape.

## Purpose

The restricted HTTP/1.1 profile (ADR 0186 §D6): its codec, and `HandleLoop`,
the adapter that serves it on a loop group. `Parser.Parse`
frames a request out of a `[]byte` into a reused `corenet.HTTP1RequestValue`,
and the writer appends a response, or the canned answer to a refusal, to a
`[]byte`. It does no I/O and owns no connection. Its request value, response
value, limits and handler port are `internal/core/net`'s, declared in
`design/net.yaml` (ADR 0160 §4: the value in the core, the wire format here).

`HandleLoop(h, p)` returns the `*LoopHandler`, a `corenet.LoopHandler`: it calls
`Parse` from `OnData`, runs the `HTTP1Handler`, answers a refusal and closes,
turns the engine's soft pressure into `Connection: close`, and writes the
batch's answers at `OnBatchEnd`, after the handler's `HTTP1Batcher.EndBatch`.
Published by `pkg/v1/net/http1` (ADR 0186 §D9 step 12).

A handler that is a `corenet.HTTP1BodyReader` (#844, `http1.BodyReader`) takes its
body as it arrives, chunk by chunk, the spill holding only the head; any other
keeps its whole request in the spill, so a group's `SpillCap` and the parser's
`Body` bound agree at Start (`CheckSpill`).

`Files` (#982) serves a route's tree from the lane's worker: a file answer is a
pending one (R3) whose `HTTP1FileValue` leaves by `corenet.LoopFileWriter` when the
backend has it and the file a descriptor, through a chunked copy on the worker otherwise
(portable, embed.FS, a refusal), the descriptor or reader closed on every path; the
worker, the canceller and any handler behind it are found through the chain
(`findWorker`, `findCanceller`). Reused from `net/static`, unexported there: the
pinned types and the name rules — to share later.

The chain (ADR 0191; `Trace` since #855, `RequestID` since #983, `CORS` since #854, `RateLimit` since #860): `Routes`, `Bearer`, `Headers`, `Log`, `Trace`, `RequestID`, `CORS` and `RateLimit` each return a
`corenet.Middleware[corenet.HTTP1Handler]`, composed by `corenet.Chain`; every
link reads `Path`, the canonical target `Parse` computed, never `Target`.

## Limits

| Bound | Default | Maximum | Refusal |
|---|---|---|---|
| head (request line + fields + blank line) | `HTTP1DefaultHead` 8192 B | `HTTP1MaxHead` 65536 B | 431 |
| field lines | — | `HTTP1MaxFields` 64 | 431 |
| request-target | `HTTP1DefaultTarget` 2048 B | the head bound | 414 |
| Content-Length | `HTTP1DefaultBody` 1 MiB (on a loop group, capped at `SpillCap - Head` for a handler that takes the body whole, #844) | `HTTP1MaxBody` 1 GiB | 413 |
| Content-Length digits | — | `HTTP1MaxContentLengthDigits` 10 | 400 |

The defaults are the C reference's (`HTTP_MAX_*` in `http_parse.h`), so the
two parsers answer the same corpus alike. A zero limit is the default, a
negative one or one past its maximum is `HTTP1Misconfigured` at `NewParser`.

## Cost

`BENCH.md` holds the figures (Apple M1 Pro, busy): `Parse` frames the
challenge's GET in about 165 ns with 0 allocations, where `net/http.ReadRequest`
takes about 1.4 µs and 11 allocations; `AppendResponse` about 16 ns. On the C
reference's own stream the codec is about 2.1× `http_parse.c` (249 against
118 ns a request) — a diagnostic, never a gate. Budgets
(`design/sdk.yaml`) are allocation counts only — `Parse`, `AppendResponse`,
`AppendStatusLine`, `AppendField`, `AppendRefusal`, `(*LoopHandler).OnData`,
`(*LoopHandler).OnBatchEnd` and the links' `ServeHTTP1` at 0 — never
nanoseconds (ADR 0165). A batch of
sixteen pipelined GETs through the adapter costs about 107 ns a request,
deferred ones about 110.

## Error range

None of its own: every sentinel is `internal/core/net`'s, `0.2.11.45` –
`0.2.11.55` (`HTTP1*`).

## Do NOT

- Hand a refused request to a handler, or let a handler write a refusal's
  answer. The refusal is the codec's, and so is the close.
- Make framing depend on the method or the route. `Consumed` is head plus
  Content-Length, always.
- Accept a Transfer-Encoding "because it is identity". Any value is refused.
- Read past `len(buf)`. The scans are bounded by `min(len, head)`; the fuzz
  fills the capacity with `\r\n\r\n` to catch a read beyond it.
- Wrap a sentinel on a path that runs per request.
- Keep a response value or a request's bytes past `OnBatchEnd`, or append to
  a pooled response's slices: they are the handler's.
- Frame a streamed answer by anything but the connection's close: `Stream` is
  ADR 0186 §D6's amended response framing, not a new one.
- Add CONNECT or TLS here; Upgrade only as `websocket.HandleLoop` does (ADR 0186 §D6, amended): out of the profile (ADR 0186 §D6). HTTP/2 is
  the sibling profile, `internal/service/net/http2` (ADR 0221).

## Verification

```
cd internal && GOWORK=off go test -race ./service/net/http1/
cd internal && GOWORK=off go test -run Perf ./service/net/http1/      # the allocation lane
cd internal && GOWORK=off go test -run '^$' -fuzz FuzzDifferential -fuzztime 60s ./service/net/http1/
cd internal && GOWORK=off go test -run '^$' -fuzz FuzzParseProperties -fuzztime 60s ./service/net/http1/
```

## Reference

- ADR 0186 §D6 (the profile), §D9 (publication order); ADR 0160 §4; ADR 0029 D3.
- ADR 0191 §D3 (the canonical target), §D4 (the chain).
- RFC 9110 §5.6.2 (tchar), §10.1.1 (Expect), §15; RFC 9112 §2.2, §3, §5, §6, §11.
- `bench/cref/src/http_parse.{c,h}`, `parse_test.c` — the C reference, same scope.
