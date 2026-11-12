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

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — what the profile refuses, the framing rule, a usage sketch |
| `parse.go` | `Parser` (`NewParser`, `Limits`, `Parse`), `Continue`, `Pending`; the request line, field and Content-Length rules; `reset` clears what a link sets too (`Route`, `Auth`, `Authenticated`) |
| `target.go` | `Canonical` (exported for an HTTP/2 codec, #962): `Path` and `Query` once the head is whole — origin-form, absolute-form reduced once its authority equals Host, `*` for OPTIONS — and the three target refusals (ADR 0191 §D3) |
| `chain.go` | the chain's links: `Routes` (`RouteValue`, `RouteLayer`), `Bearer` (`BearerLayer`), `Headers` (`HeadersLayer`), `Log` (`LogOption`, `LogAllow`, `LogSlowerThan`, `LogSample`, `LogClock`, `LogLayer`: a record per request, `ID`, `RequestID` and `Elapsed` from the batch's start filled, and a deferred answer's second record, `Completed`, at the batch's end), `Trace` (`TraceLayer`, #855: `req.Trace` read from `traceparent`, an invalid one left invalid and the request served, `res.TraceLine` written, the same trace, on the answer; no sampling, no export; a Pending answer does not carry it), `RateLimit` (`RateKey`, `RateOption`, `RateExempt`, `RateClock`, `RateLimitLayer`, #860: a corenet.HTTP1Limiter asked per request, 429 with `Retry-After` and `Connection: close` past the budget, the clock read once a batch), `CORS` (`CORSLayer`, #854, a `corenet.CORSPolicy` built at construction: a preflight answered 204 by the link, ahead of every link after it, a listed origin the policy's prebuilt fields, an unlisted one `Vary: Origin` alone), `RequestID` (`RequestIDOption`, `RequestIDMint`, `RequestIDLayer`, #983: a client's `X-Request-Id` of 1 to 64 visible ASCII bytes kept, anything else replaced by `HTTP1RequestValue.ID` in hexadecimal, the request served either way; copied by value into the request and the answer, a deferred, a pending or an adapter's 500 included); each layer forwards `EndBatch` |
| `trace_external_test.go`, `trace_alloc_external_test.go` | the link: a valid field, the malformed ones left invalid and served, a deferred answer keeping its field, the access record's trace id, field lengths 0..64; (race off) the link allocates nothing; `FuzzParseProperties` seeded with `traceparent` fields of every length 0..64, `checkTraceField` |
| `ratelimit.go` | #860: `KeyedBuckets` (`NewKeyedBuckets`, `KeyedBucketsValue`), the token-bucket table — fixed entries, an open-addressed index repaired by backward shift, an LRU list through the entries, all allocated at construction, one lock below 8 192 keys so its LRU is exact and 64 shards from there |
| `ratelimit_external_test.go` | the acceptance (the 11th of one subject refused, Retry-After: 1, another subject served, 2 000 subjects on a table of 1 000 with the memory unchanged and the oldest forgotten), every key kind, an exempt route, the refusals, a backward clock, and a random mix compared with a plain LRU |
| `files.go` | #982: `Files`, `FilesLayer` — a route's tree on the loop: GET/HEAD, on the lane's worker (open, stat, `Lstat` of every element), the job entries reused from a per-lane table, 0 allocations on the lane's path; `resolve`, `fileAnswer` (304, one Range, 416, `index.html`, the SPA shell, a redirect to the directory's slash) |
| `file_body.go` | #982: the body of a file answer once its head is out: `startFile` (the backend's `LoopFileWriter`, or the copy path), `copyDone`, `writeChunk`, `fileWritable`, `closedFile`; `jobCanceller`, the job entries given back on every path; `park` and `unpark`: the answers flushed right after a copied file (`bodyAfter`, `fileLeft`, `afterFile`) — the head of another file, or held bytes — that the output refuses while the file's last chunks still fill it wait in `tail` for OnWritable (`bodyParked`) under the copy path's stall, instead of closing the connection, which the portable backend's writer, draining apart from the loop, made happen in 3 % of `TestFiles_copyPathAndPipeline`'s runs (`TestFiles_answersAfterAFullOutput`, a 64 KiB ceiling, fails without it); a refusal anywhere else still closes. The body (`fileBody`, 72 bytes) is a pointer on `connFlag`, allocated by a slot's first file answer and kept for its later connections (`setFile`, `clearFile`): inline, it raised an idle connection past #859's 512 B budget (`websocket`'s `TestLoopWS_tenThousandIdleConnections`: 542 B on epoll, 551 B on io_uring, 463 B and 472 B as a pointer) |
| `stream.go` | #858: `openStream`, `announceStreams`, `OnDrain`; a Stream answer's head goes out in the batch, the `HTTP1Streamer` found through the chain (`findStreamer`, `link.wrapped`) is told once it has |
| `stream_external_test.go` | the head and a thousand events read by net/http, a pipelined request dropped, every refusal answered 500, timeout, drain and close reaching the handler, a chain in front of it |
| `lanes.go` | `laneTable`, a link's per-lane state found with no lock, built the first time a lane comes (the adapter's own pattern, for a link that keeps state between the requests of a batch) |
| `access_external_test.go` | #848: the manual clock's acceptance (a deferred answer logged once with `Status` and `Elapsed` ≥ 2 ms, a 100 µs answer not at all), the batch's stamp, the record pair of a deferred answer, `ID` per lane, `LogSample` and its combination with a threshold, the options' refusals |
| `cors_external_test.go` | #854: a listed preflight's fields, every refusal (an unlisted origin or method, case, a suffix, `null`) answered with no CORS field, actual requests, the wildcard and credentials, the age's clamp, the safe default methods, a deferred or a pending answer keeping its fields, every construction refusal named |
| `requestid_external_test.go` | the link on the adapter: echoed and in the record, minted distinct over two lanes, hostile identifiers replaced and served (a control byte is the codec's own 400), `RequestIDMint`, the value's gate, `AppendResponse`'s field, the adapter's 500 and a worker's answer carrying it |
| `refusal.go` | `Refusal` and its 26 values; `Status`, `Err` (the core sentinel), `String` |
| `write.go` | `AppendStatusLine`, `AppendField`, `AppendResponse`, `AppendRefusal`; the precomputed status lines |
| `loop.go` | `HandleLoop`, `LoopHandler` (`Unwrap` returns the `HTTP1Handler`, so a group may take TLS on the portable backend, ADR 0198; `MarkServed`, the embedding HTTP/2 adapter's requests counted so the batch ends once): the loop adapter — per-lane state built at `OnStart` (`loopState`: the request value, the batch's answers in `out`, the pooled response values, a flag per connection by ring and slot), deferred answers completed in request order (`complete`), one `Write` per connection run (`flush`) |
| `loop_body.go` | #844: `CheckSpill` (`corenet.LoopSpillChecker`, a loop group's Start): a default `Body` bound capped at `SpillCap - Head`, an explicit one past it refused `HTTP1Misconfigured` naming `body`; `openBody`, `feedBody`, `abortBody`: a handler that is a `corenet.HTTP1BodyReader` is given its body chunk by chunk (each chunk valid for its call, the head copied into the connection's own `HTTP1RequestValue`, so `req` stays valid across `OnData` calls), `ServeHTTP1` running after the last chunk with `Body` nil; a body reader's Content-Length is bounded by `Limits.Body`, not the spill; a close mid-body calls `HTTP1BodyAborter.AbortBody`, and the chain's links do not forward either port |
| `loop_body_external_test.go` | the spill against the bound (413 on the head, the explicit refusal naming `body`), a 1 MiB body in chunks across `OnData` calls with a pipelined request behind it, the abort, a closing answer; `loop_body_alloc_external_test.go` (race off): a batch of a body reader's chunks allocates nothing |
| `loop_pending.go` | the adapter's pending answers (R3, ADR 0194): `LoopHandler` is a `corenet.LoopOffloader` when its handler is an `HTTP1Worker` (`pendingWorker`); `hold` keeps a connection's answers in order behind its first pending one (`heldItem`, the flag's own buffer, compacted on every partial flush so `OutputCap` bounds only what is still held → close); a deferred answer a closed connection gives up waits in `dropped` until `EndBatch` has returned, never back in the pool while the batcher holds it; `pendSlot` (fixed, by pointer, its own generation, `cancelled` on close); a full queue's 503 + `Retry-After: 1`; `completeHeld`/`flushReady` at the batch's end; `Pending` for the drain |
| `loop_pending_external_test.go` | pending answers on a fake outbound: an open pipeline (a pending answer always at the tail) kept open under the ceiling, every answer in order; a deferred answer given up mid-batch — past the ceiling or by `OnClose` — never handed to another connection's request of the same batch; a stale `OnDone` frees nothing |
| `loop_pending_alloc_external_test.go` | (race off) a slot's churn reuses the held list and the buffer the last connection left: 0 allocations a connection |
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
| Content-Length | `HTTP1DefaultBody` 1 MiB (on a loop group, capped at `SpillCap - Head` for a handler that takes the body whole, #844) | `HTTP1MaxBody` 1 GiB | 413 |
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
- **An upgrade is the one other way past the codec.** `res.Upgrade` writes a 101 and
  hands every later byte to `HTTP1Upgrader.OnUpgradedData`: the codec never again
  parses a byte of that connection, so the profile's refusals stop at the 101,
  stated. Bytes that arrive with the upgrade request stay in the spill until the
  101 has left, so nothing can be answered before it.
- **A stream ends with the connection.** A response is delimited by close only when
  the handler asked for it (`res.Stream`), and then nothing behind it is read: a
  pipelined request is dropped with the connection, which is the only honest framing
  once a body has no length. `HTTP1StreamValue.Send` is `LoopIO.Write`, so the
  output ceiling binds a stream that stopped reading exactly as it binds an answer.
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
  allocates. `Canonical` runs once the head is whole, since absolute-form is
  judged against Host; the refusal still depends only on head bytes, so the
  codec stays monotone.
- **A link never frames.** The adapter advances by the `n` `Parse` returned,
  before the handler runs; `Consumed`, `HeadLen` and `Body` written by a link
  change nothing (`TestChain_onTheLoop`).
- **The links allocate nothing, by shape.** `Routes` compares bytes with the
  table built once; `Bearer` lets the verifier write the request's own
  `HTTP1AuthValue`, so no value of its own escapes to the heap; `Headers`
  hands out its bytes with no spare capacity, so an append copies rather
  than writes into them; `Log` passes its record by value and keeps a deferred one's in its lane's reused list. A 401 behind
  `Headers` copies its field after the link's: the one error path that
  allocates, named in the budget.
- **A refusal's delay is a value too.** `HTTP1ResponseValue.RetryAfter` is written
  by the codec as `Retry-After`, like `RequestID`: a 429's bytes never have to be
  built per request. The bucket table never stores a key's bytes, only its 64-bit
  hash under a random seed, so a client choosing keys cannot aim collisions, and
  evicting an entry forgets a bucket without freeing anything. An attacker who
  cycles past the table's capacity gets a fresh bucket each time: the bound is on
  memory and on any one key, never on the sum of all keys.
- **CORS has its own response slot.** `HTTP1ResponseValue.CORS` holds the policy's
  prebuilt lines, written by the codec after `Header`: appended to `Header` they
  would be copied whenever a `Headers` link had run first, one allocation per
  request. The origin is compared byte for byte against the configured list and
  the bytes sent are the list's, never the request's, so nothing the client sent
  is echoed. The refusals are by construction (ADR 0031): `*` with `Credentials`,
  `null`, an origin that is not one exact lower-case `scheme://host[:port]`, no
  origin at all, a method or field that is not a token. A preflight is the link's
  own answer so that a `Bearer` link behind it never refuses one (browsers send
  no credentials on a preflight).
- **A per-request field is a value the codec writes.** `Headers` shares one
  read-only slice because its bytes never change; an `X-Request-Id` differs per
  request, so a slice of its bytes would have to outlive the batch for a deferred
  answer. `HTTP1ResponseValue.RequestID` is an array the response carries, and
  `AppendResponse` writes it after `Header`. `HTTP1RequestIDValue.Set` is the gate
  (1 to 64 bytes of 0x21 to 0x7E): nothing else enters a value the codec puts on
  the wire, so a handler cannot split a response with it.
- **`Log`'s filters judge each record.** `LogSlowerThan` keeps what reached the
  threshold, `LogSample` one in n by the request's counter, and with both, every
  slow record and one in n of the rest; the deferred answer's second record is
  judged on its own `Elapsed`, the time since it was deferred, so a slow completion
  is logged though its first record, immediate, was not. A record's `Elapsed` runs
  from the batch's start (one stamp a batch, reset at `EndBatch`, what a request
  waited behind the batch's earlier ones) to the record's own reading of the clock:
  one read per record. The issue's "zero extra reads" would leave an immediate
  answer's `Elapsed` always zero, which is the one number the operator asked for.
- **A request's number is the adapter's, not a link's.** `HTTP1RequestValue.ID`
  (lane in the top 16 bits, the lane's counter below) is counted once by the lane
  that frames the request, so `RequestID` and `Log` read one number, no link keeps
  a counter, and two lanes cannot collide.
- **Every layer forwards `EndBatch`.** `HandleLoop` finds the batcher on the
  handler it is given, the chain's outermost layer; a layer that did not
  forward would leave every deferred answer deferred, answered 500. A link a
  program writes itself must do the same.
- **One `EndBatch` a batch, whichever version served it.** `net/http2`'s adapter
  embeds this one; it reports its own requests with `MarkServed` before
  `OnBatchEnd`, and this adapter's `OnBatchEnd` calls the batcher once for both
  versions, before either writes an answer — `HTTP1Batcher`'s contract. It used
  to be called twice on a lane that served both, the second time after the
  HTTP/1.1 answers had been written: a handler that commits a transaction per
  batch committed an empty second one.
- **Requests are counted once a batch (#966).** Each lane's own requests are
  added to an atomic at the batch's end, never one per request; `HTTPCounts`
  (`corenet.LoopHTTPCounter`) sums the lanes for State's `HTTP1Requests`.
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
