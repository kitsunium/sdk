<!-- updated: 2026-10-26T12:00:00Z -->
# internal/service/net/http1/

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

The chain (ADR 0191): `Routes`, `Bearer`, `Headers` and `Log` each return a
`corenet.Middleware[corenet.HTTP1Handler]`, composed by `corenet.Chain`; every
link reads `Path`, the canonical target `Parse` computed, never `Target`.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — what the profile refuses, the framing rule, a usage sketch |
| `parse.go` | `Parser` (`NewParser`, `Limits`, `Parse`), `Continue`, `Pending`; the request line, field and Content-Length rules; `reset` clears what a link sets too (`Route`, `Auth`, `Authenticated`) |
| `target.go` | `canonical`: `Path` and `Query` once the head is whole — origin-form, absolute-form reduced once its authority equals Host, `*` for OPTIONS — and the three target refusals (ADR 0191 §D3) |
| `chain.go` | the chain's links: `Routes` (`RouteValue`, `RouteLayer`), `Bearer` (`BearerLayer`), `Headers` (`HeadersLayer`), `Log` (`LogLayer`); each layer forwards `EndBatch` |
| `refusal.go` | `Refusal` and its 26 values; `Status`, `Err` (the core sentinel), `String` |
| `write.go` | `AppendStatusLine`, `AppendField`, `AppendResponse`, `AppendRefusal`; the precomputed status lines |
| `loop.go` | `HandleLoop`, `LoopHandler` (`Unwrap` returns the `HTTP1Handler`, so a group may take TLS on the portable backend, ADR 0196): the loop adapter — per-lane state built at `OnStart` (`loopState`: the request value, the batch's answers in `out`, the pooled response values, a flag per connection by ring and slot), deferred answers completed in request order (`complete`), one `Write` per connection run (`flush`) |
| `loop_fixtures_test.go` | the adapter's batch fixture (`perfLoop`, `perfH`) and the request bytes, untagged so the benchmarks reach them under race too |
| `loop_external_test.go` | the adapter on a fake `LoopIO`: order of immediate and deferred answers, every way a connection closes, HEAD, slot reuse, several loops |
| `target_external_test.go` | the canonical target: what each form reduces to, every refusal, and `Parse` resetting what a link set |
| `chain_external_test.go` | each link — routes matched, 401s, the subject copied (the token's bytes overwritten after), fields never written into, records without a credential or the query, every misconfiguration — and a chain behind the adapter: deferred answers through every layer, a link writing `Consumed` changing nothing, the lane |
| `corpus_external_test.go` | every vector of the C reference (`bench/cref/src/parse_test.c`), the profile's own refusals, the smuggling vectors, limits, prefixes, framing, early answers, pipelining |
| `fuzz_external_test.go` | `FuzzParseProperties`, `TestMutations` (the C reference's mutation loop), `FuzzDifferential` against `net/http.ReadRequest` |
| `write_external_test.go` | the writer, read back by `net/http.ReadResponse` |
| `perf_fixtures_test.go`, `gen_perf_test.go` | the `allocs: 0` budgets of `design/sdk.yaml` (race off) |
| `http1_bench_test.go` | the figures `BENCH.md` records, against `net/http.ReadRequest` |

## Limits

| Bound | Default | Maximum | Refusal |
|---|---|---|---|
| head (request line + fields + blank line) | `HTTP1DefaultHead` 8192 B | `HTTP1MaxHead` 65536 B | 431 |
| field lines | — | `HTTP1MaxFields` 64 | 431 |
| request-target | `HTTP1DefaultTarget` 2048 B | the head bound | 414 |
| Content-Length | `HTTP1DefaultBody` 1 MiB | `HTTP1MaxBody` 1 GiB | 413 |
| Content-Length digits | — | `HTTP1MaxContentLengthDigits` 10 | 400 |

The defaults are the C reference's (`HTTP_MAX_*` in `http_parse.h`), so the
two parsers answer the same corpus alike. A zero limit is the default, a
negative one or one past its maximum is `HTTP1Misconfigured` at `NewParser`.

## Why-this-shape

- **Refusals are named once and answered once.** `Parse` returns a `Refusal`
  and consumes nothing; `AppendRefusal` writes its status, `Content-Length: 0`
  and `Connection: close` from a precomputed string, and the caller closes. No
  handler runs, so none can answer a smuggled request in its own way. Each
  refusal maps to one `0.2.11.*` sentinel (`Err`), grouped by the decision a
  caller makes; the `Refusal` is what a log line names.
- **Framing is the codec's and depends on nothing else.** `Consumed` is
  `HeadLen + ContentLength`, set as soon as the head is whole, before the body
  arrives: an early 401, 404 or 400 knows what it must drain (`Pending`) or
  that it should close instead. A GET carrying a Content-Length takes its body
  like a POST; a body that spells a request is never framed as one
  (`TestFraming`, `TestEarlyAnswer`).
- **The response's framing is the codec's too.** A handler fills
  `HTTP1ResponseValue` — status, extra fields written by `AppendField`, body —
  and `AppendResponse` writes the status line, `Content-Length` from
  `len(Body)` and `Connection: close` when the request, the response or the
  caller asks for it. `AppendField` refuses `Content-Length`, `Connection` and
  `Transfer-Encoding`, so a handler cannot spell a second, disagreeing frame.
- **Every Transfer-Encoding is refused, not only chunked.** The profile frames
  by Content-Length alone; accepting any coding means choosing between two
  framings, which is the smuggling vector itself (RFC 9112 §11.2). Trailers
  only travel in a chunked body, so they are refused with it. A second
  Content-Length is refused even when it repeats the first.
- **Monotone by construction.** A refusal depends only on bytes up to the one
  that broke a rule, and a scan that runs out of bytes says "more" unless the
  head bound is spent. So more bytes never turn a refusal into an acceptance,
  and an accepted request takes the same bytes whatever follows it; the fuzz
  checks both on every input and every prefix.
- **Stricter than the C reference in three places, all refusals.** A control
  byte other than the tab, or DEL, in a value (RFC 9110 §5.5 `field-vchar`); a
  control, DEL or high byte in the target (RFC 9112 §3.2 is ASCII); CONNECT
  (501). The C reference accepts those; nothing it refuses is accepted here.
- **Not stricter than the C reference on Host.** A request with no Host is
  accepted, as the C reference does (its framing vector pipelines one): the
  profile serves one origin and routes by target. RFC 9112 §3.2 says a server
  MUST answer such a request 400; the adapter may refuse it, the codec
  reports `HasHost`. The differential fuzz lists this as a class where
  `net/http` refuses and the codec accepts.
- **`Fields` is an array.** A slice would allocate the first time a reused
  value meets a nil one; 64 entries of two slice headers is 3 KiB per
  connection-loop, reused. `reset` clears the scalars and leaves the stale
  entries past `NFields`, which nothing reads: clearing 3 KiB per request
  would cost more than the parse.
- **Word scans where the run is long, a table where it is short.** The target
  and every value go through `internal/kernel/swar` (`Set.Index`, first match
  only, a tab hit resumes the scan); the method and names are a few bytes and
  go through a 256-entry `tchar` table.
- **The writer returns the preallocated sentinel**, never a wrapped one: a
  refusal on the hot path costs no allocation (ADR 0186 §D4). `NewParser`
  wraps with the field it refused, since it runs once.

- **The adapter's state is per lane, found by `LoopIO.Loop`.** A table of
  `*loopState` behind an `atomic.Pointer`, built for every lane at `OnStart`
  (ADR 0191 §D1); a lane past the count, or an adapter driven without
  `OnStart`, still builds its own under a mutex the first time it calls. The
  data path reads it without a lock. A lane touches only its own entry.
- **One canonical target, computed once by the codec** (ADR 0191 §D3). A
  router and an authenticating link that read two targets is how a route
  escapes its check: `/health/../api`, `//api`, `%2e%2e`, `%2f`, an
  absolute-form whose authority is not the Host. `Parse` refuses every one
  with 400 and hands each link the same `Path`, still encoded: no escape left
  in it can spell a separator, so nothing has to be decoded, and nothing
  allocates. `canonical` runs once the head is whole, since absolute-form is
  judged against Host; the refusal still depends only on head bytes, so the
  codec stays monotone.
- **A link never frames.** The adapter advances by the `n` `Parse` returned,
  before the handler runs; `Consumed`, `HeadLen` and `Body` written by a link
  change nothing (`TestChain_onTheLoop`).
- **The links allocate nothing, by shape.** `Routes` compares bytes with the
  table built once; `Bearer` lets the verifier write the request's own
  `HTTP1AuthValue`, so no value of its own escapes to the heap; `Headers`
  hands out its bytes with no spare capacity, so an append copies rather
  than writes into them; `Log` passes its record by value. A 401 behind
  `Headers` copies its field after the link's: the one error path that
  allocates, named in the budget.
- **Every layer forwards `EndBatch`.** `HandleLoop` finds the batcher on the
  handler it is given, the chain's outermost layer; a layer that did not
  forward would leave every deferred answer deferred, answered 500. A link a
  program writes itself must do the same.
- **Answers leave at the batch's end, in request order.** An immediate answer
  is written into the loop's `out` when `ServeHTTP1` returns, so the handler's
  bytes need live no longer; a deferred one (`Defer`) keeps its pooled
  response value until `EndBatch` returns. When a batch deferred anything,
  `complete` writes every answer again into `spare` in request order, so a
  connection's answers still leave in one `Write`. The engine flushes after
  `OnBatchEnd` anyway, so the wait adds no latency.
- **Whether a connection closes is known when the request is served** — the
  request's `Connection: close`, the response's `Close`, the loop's pressure,
  a refusal — and nothing after it is read (`connFlag.closing`). A deferred
  answer left deferred, or a status the writer refuses, becomes a 500 that
  closes at the batch's end; requests pipelined behind it in the same batch
  were already served, and their answers are dropped with the connection.
- **A refused `Write` closes the connection rather than queue.** Queuing
  would hold the answer's bytes per connection; `OutputCap` bounds what one
  connection's answers to one batch may hold.
- **Response values are pooled per loop and handed out zeroed**, `Header` and
  `Body` nil: a pooled slice is never appended to again, since it may be the
  handler's own buffer.
- **No option is honoured by the adapter.** `ReadHeaderTimeout` and
  `MaxHeaderBytes` on a loop group are refused at `Start`; the head bound is
  the `Parser`'s, and a slow head is bounded by `IdleTimeout` and the spill.

## Cost

`BENCH.md` holds the figures (Apple M1 Pro, busy): `Parse` frames the
challenge's GET in about 165 ns with 0 allocations, where `net/http.ReadRequest`
takes about 1.4 µs and 11 allocations; `AppendResponse` about 16 ns. On the C
reference's own stream the codec is about 2.1× `http_parse.c` (249 against
118 ns a request) — a diagnostic, never a gate. Budgets
(`design/sdk.yaml`) are allocation counts only — `Parse`, `AppendResponse`,
`AppendStatusLine`, `AppendField`, `AppendRefusal`, `(*LoopHandler).OnData`,
`(*LoopHandler).OnBatchEnd` and the four links' `ServeHTTP1` at 0 — never
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
- Add HTTP/2, CONNECT, Upgrade or TLS here: out of the profile (ADR 0186 §D6).

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
