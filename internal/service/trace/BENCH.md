<!-- generated from internal/service/trace/{http_server,recorder}_bench_test.go — see §5 for the exact run procedure; a single `-count=3` is NOT it -->
# Benchmarks — `internal/service/trace`

`pkg/v1/trace/BENCH.md` prices a SPAN: 728.4 ns sampled, 403.5 ns unsampled,
3 allocations either way. This file prices the two units this package owns that
a span benchmark never touches, and that the package's own `CLAUDE.md` named as
unmeasured:

1. **`serveTraced`**, which runs once per HTTP **request** rather than once per
   span, and adds five heap escapes of its own on top of `Start`'s.
2. **`Recorder.record`**, which takes an exclusive lock on every span end from
   every request goroutine — and which held that lock on an `RWMutex` until
   these numbers were taken.

Both produced a change. Both changes are stated with what they bought and what
they gave up.

## Reproducibility envelope

> **Numbers vary across machines, and this file was bitten by exactly that.**
> §3.3 records a ratio that was thrown away because its baseline came from a
> different box — one 2.5× faster on the same benchmark. Allocation counts are
> a property of the code and travel; nanoseconds do not.

| Dimension | Value |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor |
| CPU cores available | 8 |
| RAM | 15 GiB |
| OS / kernel | Linux 6.12.101+deb13-amd64 |
| Architecture | amd64 |
| Go toolchain | go1.27.1 linux/amd64 |
| Git branch | `jaimerias-que-tu-te-connect` |
| Git commit | `bee0751` (+ the changes in §1.4 and §2.6) |
| Generated (UTC) | 2026-09-10 |
| Machine load (`uptime`) | 1-minute average between **0.05 and 1.68** across the campaign, sampled before and after every table |

**Every number below is the median of at least NINE samples taken across at
least THREE separate processes** — five processes × `-count=3` for the
contended recorder rows. A single-process `-count=3` was demonstrated on this
box to report a precision it does not have: §3.1 is a row-set it produced in
which a lock measured 36 % SLOWER than a strictly more expensive one.

## 1. `serveTraced` — the cost of one traced HTTP request

Every arm runs the **identical handler body** (`w.WriteHeader(200)`) through the
identical `http.Handler` interface call against the identical reused request and
a `ResponseWriter` that allocates nothing. The delta is the middleware.

### 1.1 The four shapes a request can take

| | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| **Untraced control** — no middleware at all | **5.101** | **0** | **0** |
| Traced, root (no inbound header), **sampled** | 2 098 | 1 720 | **12** |
| Traced, root, **unsampled** | 1 154 | 888 | **10** |
| Traced, child (valid `traceparent`), **sampled** | 2 346 | 1 736 | **13** |
| Traced, child, **unsampled** | 1 391 | 904 | **11** |

The control is 5.1 ns and zero allocations, so *the whole* of every other row is
the middleware and nothing has to be subtracted.

**The rows check each other twice, and both checks are exact:**

- child − root is **+1 allocation and +16 B**, in the sampled pair *and* in the
  unsampled pair. That single allocation is the second `Header.Get`: a request
  with no parseable `traceparent` never reaches `ParseTraceState`, so it asks
  `http.Header` for one key instead of two. 16 B is the size class an
  eleven-byte string lands in.
- sampled − unsampled is **+2 allocations and +832 B**, in the root pair *and*
  in the child pair. That is the recording span, and it is the same in both
  because the sampling decision is taken once at the root and travels
  (ADR 0051): a child does not re-decide, it inherits.

A table where two independent differences come out byte-identical in both pairs
is a table that is measuring one thing.

### 1.2 The number that decides something

Latency and allocation rate give **opposite** answers, and only one of them
matters.

Measured on **this box**, `//internal/service/net/server`'s
`BenchmarkHTTP_Adapter` — one full keep-alive `GET` through the SDK's HTTP
stack — is **162 900 ns / 5 148 B / 63 allocs** (median of 9).

| Traced request | + ns | share of 162 900 ns | + allocs | share of 63 allocs |
|---|---:|---:|---:|---:|
| root, sampled | 2 093 | **+1.29 %** | 12 | **+19.0 %** |
| root, unsampled | 1 149 | **+0.71 %** | 10 | **+15.9 %** |
| child, sampled | 2 341 | **+1.44 %** | 13 | **+20.6 %** |
| child, unsampled | 1 386 | **+0.85 %** | 11 | **+17.5 %** |

**Tracing costs a request 1.3 % of its time and a fifth of its allocations.**

Nobody rejects a middleware over 1.3 % of latency. A **+19 % allocation rate**
is a different conversation: allocation rate is what sets GC frequency, and at
high RPS it is paid by every goroutine in the process rather than by the request
that caused it. That is the number a team should be shown when asked whether
tracing belongs in a *default* middleware stack — and it is the one
`TestServerMiddlewareStaysWithinItsPerRequestAllocationBudget` now guards.

It also says where to spend a sampling ratio: an unsampled request is **55 %**
of a sampled one in time but **83 %** of it in allocations. Turning the sampling
rate down buys much less than the latency figures suggest, because ten of the
twelve allocations are paid whether the span is recorded or not — §1.3 itemises
which.

### 1.3 Where the twelve allocations are

`go tool pprof -sample_index=alloc_objects -lines`, at `-memprofilerate=1` so
the attribution is **exact** rather than sampled — the totals below are integer
multiples of the 100 000 iterations, and they sum to the 12 `benchmem` reports:

```
      flat  flat%   sum%        cum   cum%
    200000 16.63% 16.63%     200000 16.63%  context.WithValue context/context.go:738
    200000 16.63% 33.26%     400000 33.26%  core/trace.contextWithValue internal/core/trace/context.go:30 (inline)
    100001  8.31% 41.57%     100001  8.31%  net/http.(*Request).WithContext net/http/request.go:377
    100000  8.31% 49.89%     100000  8.31%  service/trace.newSpan internal/service/trace/span.go:69
    100000  8.31% 58.20%     100000  8.31%  service/trace.serveTraced internal/service/trace/http_server.go:120
    100000  8.31% 66.51%     100000  8.31%  service/trace.serveTraced internal/service/trace/http_server.go:131
    100000  8.31% 74.83%     200000 16.63%  service/trace.serveTraced internal/service/trace/http_server.go:137
    100000  8.31% 83.14%     100000  8.31%  net/textproto.canonicalMIMEHeaderKey net/textproto/reader.go:801
    100000  8.31% 91.46%     100000  8.31%  slices.Clone[…AttrValue] slices/slices.go:362 (inline)
    100000  8.31% 99.77%     100000  8.31%  slices.Insert[…AttrValue] slices/slices.go:152
```

| # | Site | Why it is there |
|---:|---|---|
| 1-2 | `context.WithValue` ×2 | one `valueCtx` per `ContextWithSpanContext` — and there are **two**, §4.2 |
| 3-4 | `contextWithValue` ×2 | boxing a `SpanContextValue` into the `any` parameter, once per call |
| 5 | `Request.WithContext` | the shallow request copy `http.Handler`'s contract requires |
| 6 | `newSpan` span.go:69 | the `&span{}` itself |
| 7 | http_server.go:120 | the four-attribute `[]AttrValue{…}` literal |
| 8 | http_server.go:131 | `&statusRecorder{}`, §4.3 |
| 9 | http_server.go:137 | the `SetAttrs(…)` variadic slice |
| 10 | `canonicalMIMEHeaderKey` | `http.Header.Get("traceparent")`, §4.1 |
| 11 | `slices.Clone` | `SortAttrs` taking ownership of the start attributes |
| 12 | `slices.Insert` | growing 4 attributes to 5 when the status is recorded |

**A third of the allocations are contexts.**

The **unsampled** arm's ten, profiled the same way, differ in exactly four
places — and one of them is a surprise:

```
    200000 19.99% 19.99%     200000 19.99%  context.WithValue context/context.go:738
    200000 19.99% 39.99%     400000 39.99%  core/trace.contextWithValue internal/core/trace/context.go:30 (inline)
    100001 10.00% 49.98%     100001 10.00%  net/http.(*Request).WithContext net/http/request.go:377
    100000 10.00% 59.98%     300000 29.99%  service/trace.(*Tracer).Start internal/service/trace/tracer.go:91
    100000 10.00% 69.98%     100000 10.00%  service/trace.serveTraced internal/service/trace/http_server.go:120
    100000 10.00% 79.97%     100000 10.00%  service/trace.serveTraced internal/service/trace/http_server.go:131
    100000 10.00% 89.97%     100000 10.00%  service/trace.serveTraced internal/service/trace/http_server.go:137
    100000 10.00%   100%     100000 10.00%  net/textproto.canonicalMIMEHeaderKey net/textproto/reader.go:801
```

`tracer.go:91` is `return …, noopSpan{context: spanContext}` — **boxing the
no-op span into the `coretrace.Span` interface allocates**, because `noopSpan`
carries a `SpanContextValue` and is wider than a word. A span the tracer decided
not to record still costs a heap object simply to be returned, and that is the
middleware-level view of the fact `pkg/v1/trace/BENCH.md` states per span.

So `sampled − unsampled = +2` decomposes as **−1 + 3**: the recording path does
NOT box (it returns a `*span`, already a pointer), and it adds the `&span{}`,
the `SortAttrs` clone and the `slices.Insert` growth. The +2 in §1.1 is a
difference of four allocations, not two, and only the profile says so.

By bytes the sampled arm is more evenly spread —
`go tool pprof -sample_index=alloc_space`:

```
    0.29GB 22.13% 22.13%     0.29GB 22.13%  slices.Insert[…AttrValue]
    0.25GB 19.33% 41.45%     0.39GB 30.15%  service/trace.newSpan
    0.25GB 18.95% 60.41%     0.25GB 18.95%  net/http.(*Request).WithContext (inline)
    0.20GB 15.07% 75.48%     1.31GB 99.69%  service/trace.serveTraced
    0.14GB 10.82% 86.30%     0.14GB 10.82%  slices.Clone[…AttrValue] (inline)
    0.09GB  7.20% 93.50%     0.16GB 12.57%  core/trace.contextWithValue (inline)
    0.07GB  5.37% 98.87%     0.07GB  5.37%  context.WithValue
    0.01GB  0.82% 99.69%     0.01GB  0.82%  net/textproto.canonicalMIMEHeaderKey
```

CPU, on the same path (`-cpuprofile`, no memory profiler attached — one taken
*with* `-memprofilerate=1` was discarded, §3.4):

```
     150ms  6.07%  6.07%      150ms  6.07%  runtime.vgetrandom
     110ms  4.45% 10.53%      110ms  4.45%  time.runtimeNow
      80ms  3.24% 13.77%      280ms 11.34%  service/trace.(*Tracer).mint
      80ms  3.24% 17.00%       80ms  3.24%  runtime.memclrNoHeapPointers
      60ms  2.43% 19.43%     1880ms 76.11%  service/trace.serveTraced
      60ms  2.43% 26.72%      110ms  4.45%  slices.insertionSortCmpFunc[…]
      50ms  2.02% 30.77%      130ms  5.26%  service/trace.(*span).End
      50ms  2.02% 32.79%      330ms 13.36%  service/trace.(*span).SetAttrs
      50ms  2.02% 36.84%       70ms  2.83%  runtime.mallocgcSmallScanNoHeaderSC6
```

Cumulatively, `runtime.mallocgc` is **19.84 %** and `runtime.gcBgMarkWorker`
**10.12 %** — so roughly **30 % of a traced request's CPU is the allocator and
the collector**. That is the same finding as §1.2 seen from the other side, and
it is why the two changes below were made on allocation count rather than on
nanoseconds.

### 1.4 What changed, and what it bought

Two allocations were removed. Both are on the SAMPLED path only, which is why
the unsampled rows are unchanged — the arithmetic check that they are the
changes claimed and not something else.

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| root sampled, before | 2 339 | 2 008 | 14 |
| after **(a)** the single-attribute fast path | 2 277 | 1 960 | 13 |
| after **(b)** the ownership transfer | **2 098** | **1 720** | **12** |
| child sampled, before | 2 558 | 2 024 | 15 |
| after (a) | 2 506 | 1 976 | 14 |
| after (b) | **2 346** | **1 736** | **13** |
| root **unsampled**, before → after | 1 146 → 1 154 | 888 → 888 | 10 → 10 |
| child **unsampled**, before → after | 1 385 → 1 391 | 904 → 904 | 11 → 11 |

**(a) `span.SetAttrs` no longer clones a single attribute** — `sortedIncoming`
in `span.go`. `coremetrics.SortAttrs` always clones, and its reason is
ownership: it sorts **in place**, so it must not sort the caller's array. One
attribute is already sorted, so there is nothing to sort and nothing to protect,
and the merge that follows only ever copies elements *out* of the slice. The
validation still runs, still on the caller's goroutine, still outside the lock —
which is the property the original comment was actually defending. Worth
**−1 allocation and −48 B**, exactly one `AttrValue`. One attribute is not a
corner case: it is what `ServerMiddleware` does with the response status on
every request.

**(b) `span.finish` TRANSFERS its slices instead of cloning them.** The code was
`Attrs: slices.Clone(s.attrs), Events: slices.Clone(s.events)`, and the reason
given was *"a sink that held the value while a late (ignored) write reallocated
would otherwise observe a tear"*. **That event cannot happen**, and the comment
says so itself: every write site returns on `s.ended` under the same mutex
`finish` sets it with, so an *ignored* write reallocates nothing. The clone was
defending against its own description of an impossibility, at one allocation and
240 B on every sampled span.

It is a transfer and not a bare read: `finish` hands the arrays over **and nils
its own references**, so there is exactly one holder afterwards — the same
guarantee the clone gave, for nothing. `Links` was already handed over
unclosed, so the shape is not new to this type.

Worth **−1 allocation and −240 B**, five `AttrValue`s, and **−7.9 %** on a
sampled root request. It is guarded by `TestTheValueASinkReceivesIsFinal`, whose
mutation had to be **compound** — removing either the `ended` check or the nil
alone leaves the property standing, which is the point.

> **This changes one row of a report this package does not own.**
> `pkg/v1/trace/BENCH.md`'s `BenchmarkStartEnd_SampledWithAttrs` is published as
> 1069.0 ns / 720 B / **5 allocs** and now measures **942.6 ns / 576 B / 4
> allocs** (median of 9, this box). The other three rows carry no attributes and
> keep their allocation counts exactly — `StartEnd_Sampled` 742.3 ns / 432 B / 3,
> `StartEnd_NotSampled` 389.7 / 176 / 3, `StartEnd_Nested` 1 995 / 1 296 / 9.
> That file needs a `make bench` from whoever owns `pkg/`; nothing in this
> package can update it.

### 1.5 What was NOT changed

See §4. Three further allocations were identified, priced, and left in place;
one of them is the largest single group in the profile.

## 2. `Recorder.record` — the lock was the wrong one

`record` takes an **exclusive** lock on every span end, from every request
goroutine. It held that lock on a `sync.RWMutex`, whose field comment read:
*"It is an RWMutex because Len and Dropped are pure reads an operator may poll
while spans are still arriving."*

Every clause of that is true. The conclusion is still backwards, because
`Collect` — the call an export actually makes — takes the **exclusive** side
too, so the shared side was serving `Len` and `Dropped` alone, once per
collection interval, while taxing the path that runs once per span.

Both arms below run the same body; the only difference is the lock. The control
type (`rwmutexRecorder`) stays in the benchmark file, because the price of a
rejected design is only checkable while both arms can be run in one afternoon on
one box.

### 2.1 The lock, isolated — recorder at capacity (medians of 15)

At capacity the critical section is one length comparison and one increment on
both arms, so whatever separates the columns is the lock and nothing else. It is
also the state a Recorder reaches at load between two collections, so it is the
documented behaviour rather than a corner.

| goroutines | `sync.Mutex` (shipped) | `sync.RWMutex` (rejected) | RWMutex costs |
|---:|---:|---:|---:|
| 1 | **33.16 ns** | 52.91 ns | 1.60× |
| 2 | **51.09 ns** | 84.46 ns | 1.65× |
| 4 | **102.7 ns** | 141.2 ns | 1.37× |
| 8 | **130.6 ns** | 166.3 ns | 1.27× |

Zero allocations on every cell of every table in §2.

### 2.2 The real body — append, drained every 1024 records (medians of 9)

| goroutines | `sync.Mutex` | `sync.RWMutex` | RWMutex costs |
|---:|---:|---:|---:|
| 1 | **335.5 ns** | 370.9 ns | 1.11× |
| 2 | **441.9 ns** | 508.1 ns | 1.15× |
| 4 | **581.6 ns** | 614.3 ns | 1.06× |
| 8 | **636.3 ns** | 691.0 ns | 1.09× |

**Cross-check against §2.1**: the absolute lock deltas are 35.4 / 66.2 / 32.7 /
54.7 ns here against 19.8 / 33.4 / 38.5 / 35.7 ns there — same sign, same order
of magnitude, on a body several times larger. The tables agree.

`benchmem` reports **0 allocs/op** for these rows and **~1 KB B/op**, which is
not a contradiction and is worth naming: `append` grows geometrically, so 1 024
records cost about eleven allocations, and `allocs/op` divides as an integer.
The bytes are real; the count is rounded away. It is the same blindness that
made `testing.AllocsPerRun` unusable for the guards in §5.

### 2.3 The scenario the RWMutex was chosen for (medians of 15)

Spans arriving while **one operator polls `Len`** — the exact shape the field
comment named. This is where the lock had to earn its keep.

| goroutines writing | `sync.Mutex` | `sync.RWMutex` | RWMutex costs |
|---:|---:|---:|---:|
| 1 | **83.3 ns** | 228.4 ns | **2.74×** |
| 2 | **130.1 ns** | 1 131 ns | **8.69×** |
| 4 | **151.1 ns** | 1 212 ns | **8.02×** |
| 8 | **136.8 ns** | 1 228 ns | **8.98×** |

A writer's `Lock` on an `RWMutex` must drain the in-flight reader before it can
proceed, so with a reader always in flight it pays a park/unpark round trip on
**every** acquisition — about 1.2 µs, which is what the right-hand column is.
A plain `Mutex` reader and writer simply alternate on the fast path.

The poller here has a **100 % duty cycle**, which no real operator has, so this
is the worst case and not the typical one — §2.1 is the row to quote for a
Recorder nobody is watching. But it is the worst case *of the argument the lock
was chosen on*, and losing it by 9× settles the question.

### 2.4 What the change COST — `Len` with N readers and no writer (medians of 15)

The honest way to publish a trade is to give the losing side its best shot: N
goroutines doing nothing but reading, no writer at all.

| readers | `sync.Mutex` | `sync.RWMutex` | winner |
|---:|---:|---:|---|
| 1 | **23.00 ns** | 23.89 ns | Mutex, by 1.04× |
| 2 | **32.84 ns** | 43.55 ns | Mutex, by 1.33× |
| 4 | 79.46 ns | **66.18 ns** | **RWMutex, by 1.20×** |
| 8 | 93.37 ns | **75.85 ns** | **RWMutex, by 1.23×** |

**The regression is real and it is at four or more concurrent readers.** What
makes it acceptable is not that it is small: it is that `Collect` **drains**, so
this type has exactly one reader by contract — the same reason ADR 0044 gives
for a delta meter — and at that one reader the `Mutex` is marginally faster
anyway. A regression that requires four callers of a single-caller API is a
regression against a scenario the type refuses to support.

### 2.5 The same result with the roles swapped

The tables above were taken **twice**, and the second time the two locks had
changed places: in the first campaign the shipped `Recorder` (which also carries
`resource` and `scope`, so `mu` sits at a different offset) held the RWMutex and
the hand-written control held the Mutex; in the second it was the reverse.

| | run A (RWMutex in `Recorder`) | run B (Mutex in `Recorder`) |
|---|---|---|
| at capacity, RWMutex costs | 1.63× / 1.35× / 1.32× / 1.19× | 1.60× / 1.65× / 1.37× / 1.27× |
| under a poller, RWMutex costs | 3.03× / 11.2× / 8.82× / 9.59× | 2.74× / 8.69× / 8.02× / 8.98× |
| `Len`, RWMutex faster at | 4 and 8 readers | 4 and 8 readers |

Every finding survives the swap, so the effect is the **lock** and not the
struct layout, the field offset, or which arm happened to be measured first.

### 2.6 The change

`Recorder.mu` is a `sync.Mutex`; `Len` and `Dropped` take `Lock`/`Unlock`. A
lock type is invisible to every functional test, so
`TestRecorderTakesAnExclusiveLockOnEveryPath` reads the field through `reflect`
and fails the build on a change back, naming the measurement rather than
restating the reasoning — because the reasoning is the appealing part and it is
the part that was wrong.

## 3. Rows thrown away, and why

### 3.1 The append pair measured in ONE process

First run, both arms in the same process, single `-count=3`:

```
BenchmarkRecorderRecordAppend_RWMutex     275.1 ns/op    1049 B/op
BenchmarkRecorderRecordAppend_Mutex       375.6 ns/op    1049 B/op
```

The `Mutex` 36 % **slower** — while §2.1, on a strictly smaller critical
section, has it 1.6× **faster**. Identical bodies, identical byte counts: a
table that contradicts itself arithmetically.

**Cause**: this arm allocates ~1 KB/op, so whichever benchmark runs first in a
process gets a fresh heap goal and the one after it pays for the collections the
first provoked. Run one arm per process and the contradiction disappears —
three isolated processes each:

```
RWMutex   372.5   406.2   370.2
Mutex     351.6   382.9   352.3
```

`Mutex` faster, agreeing with every other row. §2.2 is the re-run. The
zero-allocation arms (§2.1, §2.3, §2.4) are immune and were verified to be
order-independent before being pooled.

### 3.2 The whole first HTTP campaign, at `-cpu=1`

Medians of 9, and internally consistent — but `GOMAXPROCS=1` forbids concurrent
garbage collection, and on a path that is 30 % allocator and collector (§1.3)
that is not a neutral setting. The sampled root read **2 735 ns** against
**2 339 ns** at `GOMAXPROCS=8`: a 17 % tax belonging to the benchmark's own
configuration. Allocation counts were identical at both settings. Re-run at 8,
which is what a server runs at.

### 3.3 The cross-machine latency ratio

`internal/service/net/server/BENCH.md` publishes 66 054 ns / 5 133 B / 61 allocs
for a full SDK HTTP request. Dividing this file's 2 098 ns by that gives
**+3.2 %**, and it was very nearly published.

The same benchmark on **this** box is **162 900 ns / 5 148 B / 63 allocs** — the
i7-1255U that produced the original is 2.5× faster on it. The cross-machine
ratio overstates the surcharge by that factor. §1.2 is measured same-box.
Allocation counts travelled almost exactly (61 → 63, one Go patch release
apart), which is the general rule: **publish allocation ratios across machines,
never nanosecond ratios.**

### 3.4 A CPU profile taken with `-memprofilerate=1`

Its top entry was `runtime.pcvalue` at 22.22 % with `runtime.tracebackPCs` at
66.07 % cumulative — the memory profiler recording a stack for every single
allocation. It describes the profiler, not the middleware. §1.3's CPU listing
is from a separate run with no memory profiler attached.

## 4. What was refused, and what it would have cost

### 4.1 `http.Header.Get("traceparent")` — 1 allocation and 87 ns, per Get

`core/trace.Extract` asks the carrier for `TraceParentHeader`, which is
`"traceparent"` — lowercase, as W3C §3.2.1 requires of a name a vendor **sends**.
`http.Header` is keyed canonically, so every `Get` canonicalises the constant,
and that is not free:

```
BenchmarkGetLower-8       128.0 ns/op      16 B/op      1 allocs/op
BenchmarkGetCanonical-8    40.9 ns/op       0 B/op      0 allocs/op
```

**87 ns and one allocation per header read**, purely for the spelling of the
key: one on a root request, two on a request that carries a `traceparent`.

It is **not fixable here**, and it is not a one-character fix there either.
Changing the constant to `"Traceparent"` would fix the `Get` and break the
`Set`, which must emit the lowercase name onto the wire for any carrier that is
not an `http.Header`. The correct fix is a second, lookup-only constant in
`internal/core/trace` — a package this work does not own.

The local workaround, a `Carrier` wrapper that maps the two keys, was refused
for a second reason: `serveTraced`'s own comment states that `r.Header` is
passed **directly**, that `http.Header`'s `Get`/`Set` pair *is*
`coretrace.Carrier`, and that there is therefore "no adapter here and none to
keep in step". Buying one allocation by hard-coding `net/http`'s canonicalisation
rule into a second place is exactly the property that comment exists to protect.

### 4.2 The doubled span context — 4 allocations, a third of the total

`serveTraced` puts the extracted parent on the context, and `Start` then puts
the new span context on it **under the same key**, shadowing the first. Two
`context.WithValue` calls and two interface boxes: **4 of 12 allocations, the
largest single group in §1.3's profile.**

The outer one exists so that `Start` inherits a header-borne parent through the
same path it inherits a locally-started one — "there is one inheritance path,
not two". Every way to remove it fails on a stated design property:

- Skipping it when the extracted parent is invalid changes **which span becomes
  the parent** when a request context already carries one. Today a request with
  no `traceparent` starts a root even under an outer local span, and
  `ServerMiddleware`'s doc commits to that ("a malformed or absent traceparent
  silently starts a new trace"). It saves two allocations on root requests only
  — the minority in a traced deployment — in exchange for a silent behaviour
  change.
- Passing the parent to `Start` directly reopens the second inheritance path
  `Start`'s doc comment refuses by name: "a parent passed by hand is a parent
  that can be the wrong one".

So the price of having exactly one inheritance path is **4 allocations per
traced request**, and this paragraph is where a future reader finds the number
before deciding it is too high.

### 4.3 Pooling `&statusRecorder{}` — 1 allocation

A `sync.Pool` would remove it. It was refused: the wrapper is handed to a
caller-supplied handler, and ADR 0051 records this exact type as the place
ADR 0047's defect was re-introduced one layer up — a writer wrapper that claims
capabilities it does not have. A pooled wrapper adds *use after the handler
returned* to the same family of failures, in a type whose entire design is one
method (`Unwrap`) chosen so it cannot lie about `Flush` and `Hijack`. One
allocation is not worth reopening that.

### 4.4 The `slices.Insert` that grows 4 attributes to 5

`newSpan` clones the start attributes to a slice with `cap == len`, so recording
the response status always reallocates. Reserving headroom in `newSpan` would
remove it — for spans that later gain attributes, and at the cost of extra bytes
on every span that does not. That is a heuristic about how callers annotate,
and there is no measurement here that supports it. Left alone; **1 allocation
and 384 B** — five attributes at 48 B land in an eight-slot array — which is the
largest single entry in the alloc-space profile at 22.13 % of the bytes.

## 5. Method

- Each benchmark function is run in **its own `go test` invocation**, three to
  five times, `-count=3` each: **9 to 15 samples across 3 to 5 processes**, and
  the median is reported. §3.1 is what a single-process `-count=3` produced.
- The recorder arms that allocate get **one process per `(arm, -cpu)` cell**,
  for §3.1's reason.
- `B/op` counts bytes **allocated**, not retained.
- Every table was checked against the others arithmetically before being
  written. §1.1, §2.2 and §2.5 record the checks; §3 records the ones that
  failed.
- The guards use a **total** `runtime.MemStats.Mallocs` delta and never
  `testing.AllocsPerRun`, whose last line divides as integers. That is not
  theoretical here: the mutation for `TestRecordingAtCapacityAllocatesNothing`
  allocated **9 times over 500 calls**, which `AllocsPerRun` reports as
  **0.0** — the guard would have passed a drop path that grows an unbounded
  slice.
- Reproduce §1 with:
  `cd internal/service && GOWORK=off go test -run='^$' -bench='ServeHTTP_<arm>$' -benchmem -benchtime=200000x -cpu=8 -count=3 ./trace/`
- Reproduce §2 with the same shape and
  `-bench='RecorderRecordAtCapacity$' -cpu=1,2,4,8 -benchtime=2000000x`.
