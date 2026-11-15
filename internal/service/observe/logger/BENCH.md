<!-- generated from internal/service/observe/logger/{builder,stack}_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./observe/logger/` to refresh -->
# Benchmarks — `internal/service/observe/logger` and the sink chain

The SDK's headline claim is **one allocation per emit**. It is stated in the
root `CLAUDE.md`, pinned by `TestPerfAllocsBuildSend`, and it had
only ever been measured for the logger core over a single terminal sink —
**not for anything a real deployment wires underneath it**.

This report measures the layers a deployment actually adds: the eight
middlewares of `middleware/*` and the four terminal sinks of `sink/*`, each
against one shared control, in one run. The encoder half lives next door in
`encoder/BENCH.md`.

It found the claim broken, and fixed it.

---

## 0. THE HEADLINE — the claim broke at three destinations, and holds again

A full `Build().Str().Int().Send()` through the text encoder into a sink chain:

| stack | before | after |
|---|---|---|
| bare sink | 1 193 ns · 128 B · **1 alloc** | 853 ns · 128 B · **1 alloc** |
| + 1 middleware (`recover`) | 1 211 ns · 128 B · **1 alloc** | 857 ns · 128 B · **1 alloc** |
| + 2 (`recover`→`failover`) | 1 226 ns · 128 B · **1 alloc** | 935 ns · 128 B · **1 alloc** |
| + 4 (`recover`→`failover`→`sample`→`multi`) | 1 250 ns · 128 B · **1 alloc** | 913 ns · 128 B · **1 alloc** |
| `recover`→`failover(2)`→`multi(2)` | 1 249 ns · 128 B · **1 alloc** | 915 ns · 128 B · **1 alloc** |
| **`recover`→`failover(4)`→`multi(4)`** | **1 389 ns · 256 B · 3 allocs** | **917 ns · 128 B · 1 alloc** |

**Depth was never the problem — WIDTH was.** Stacking middlewares four deep
cost nothing in allocations. But fanning out to **three or more** destinations,
or configuring a failover chain **three or more** long, added one heap
allocation *per such middleware, per record* — on the completely healthy path
where no branch ever failed.

Nobody had checked, because the obvious experiment (stack them deeper) is the
one that finds nothing.

### What it was

`multi.Write` and `failover.Write` each opened with a pre-sized scratchpad for
errors that, on a healthy system, never receives a single element:

```go
errs2 := make([]error, 0, len(s.branches))   // multi.go:49
collected := make([]error, 0, len(s.chain))  // failover_sink.go:52
```

`-memprofile` + `go tool pprof -lines -top` named the line and nothing else:

```
      flat  flat%   sum%        cum   cum%
  28509900 99.71% 99.71%   28509900 99.71%  multi.(*fanoutSink).Write .../multi/multi.go:49
```

`go build -gcflags=-m` says `make([]error, 0, len(s.branches)) does not escape`
— and it still allocates, because the capacity is not a **constant**. The
compiler can only substitute a stack buffer for a non-escaping variable-size
`make` while the request fits its implicit stack budget; past that it calls
`runtime.makeslice` unconditionally. Measured, that boundary sits at **three
`error` elements**:

| branches | before | after |
|---|---|---|
| 1 | 19.40 ns · 0 allocs | 17.19 ns · 0 allocs |
| 2 | 28.19 ns · 0 allocs | 25.59 ns · 0 allocs |
| **3** | **69.87 ns · 48 B · 1 alloc** | **34.53 ns · 0 allocs** |
| 4 | 84.76 ns · 64 B · 1 alloc | 42.66 ns · 0 allocs |
| 8 | 156.5 ns · 128 B · 1 alloc | 76.71 ns · 0 allocs |

So the cost was not merely an allocation — it was a **cliff**, invisible in
review, that a caller crossed by adding a third log destination.

### The fix

`var errs2 []error` — nil, appended to only when a branch actually fails.
Behaviour is unchanged (`errors.Join` of an empty slice is nil either way, and
`len(errs2) > 0` still gates the wrap); the failure path now grows by append,
which is the path that was never hot. The sibling `tee` middleware has always
been written this way, which is why it never had the defect — and why the fix
needed no invention, only the measurement that said where to apply it.

`Flush` and `Close` keep their pre-sized slates: they run once per sink
lifetime, not once per record, and the profile named neither.

| | before | after | |
|---|---:|---:|---:|
| `MW_Multi_3` | 69.87 ns · 1 alloc | 34.53 ns · **0** | **2.02×** |
| `MW_Multi_8` | 156.5 ns · 1 alloc | 76.71 ns · **0** | **2.04×** |
| `MW_Failover_FirstOK_5Branches` | 66.51 ns · 1 alloc | 17.18 ns · **0** | **3.87×** |
| `Emit_FanOut4` | 1 389 ns · 3 allocs | 917 ns · **1 alloc** | claim restored |

The failover line carries a second result. Before, a five-branch chain cost
**3.9× a two-branch chain on the path where the first branch accepted** — the
happy path was taxed in proportion to how many fallbacks you had configured for
failures that were not happening. It is now flat: 16.80 ns at two branches,
17.18 ns at five. Length of the fallback chain no longer costs anything until
something actually fails.

`TestPerfAllocsBuildSend` and the stricter
`TestT34TraceCorrelationAddsNoAllocation` (exactly 1 alloc, all three emission
paths) both still pass. Neither was touched.

---

## 1. Per-middleware cost, against one shared control

Every middleware below wraps `discardSink` — the control on line one — and
each line is `Write(ctx, record, payload)` with the record and payload built in
setup. **The delta from the control IS the middleware.** All are `0 B/op,
0 allocs/op` unless stated.

| | ns/op | vs control | allocs |
|---|---:|---:|---:|
| **`discardSink` (CONTROL)** | **7.39** | — | 0 |
| `sample` — record DROPPED | 13.27 | +5.9 | 0 |
| `failover` — first branch accepts | 16.80 | +9.4 | 0 |
| `failover` — 5-branch chain, first accepts | 17.18 | +9.8 | 0 |
| `multi` — 1 destination | 17.19 | +9.8 | 0 |
| `route` — first predicate matches | 19.95 | +12.6 | 0 |
| `route` — no match, fallback takes it | 20.25 | +12.9 | 0 |
| `sample` — record KEPT | 20.04 | +12.7 | 0 |
| `tee` — 1 primary | 24.04 | +16.7 | 0 |
| `multi` — 2 | 25.59 | +18.2 | 0 |
| `recover` — deferred recover, nothing panics | 27.90 | +20.5 | 0 |
| `failover` — first fails, second accepts | 28.70 | +21.3 | 0 |
| `tee` — 2 primaries | 31.53 | +24.1 | 0 |
| `multi` — 3 | 34.53 | +27.1 | 0 |
| `route` — 4 entries, fourth matches | 36.47 | +29.1 | 0 |
| `multi` — 4 | 42.66 | +35.3 | 0 |
| `tee` — 4 primaries | 47.72 | +40.3 | 0 |
| `multi` — 8 | 76.71 | +69.3 | 0 |
| `async` — publisher, ring not saturated | 195.6 | +188 | 0 |
| `encwrite` — AES-256-GCM seal + frame | 1 318 | +1 311 | **6** (1 728 B) |

**Cross-check (rule 3):** no line measures below the 7.39 ns control. The
cheapest is `sample` on a dropped record at 13.27 ns, which is 5.9 ns of
sampling decision and no downstream call at all — arithmetically exactly what
it should be. Nothing here is a contradiction that needed re-running.

### `sample` — the drop really is cheaper, and by exactly the right amount

| | ns/op |
|---|---:|
| record KEPT (rate 1) | 20.04 |
| record DROPPED (rate 2³⁰) | **13.27** |
| difference | **6.77** |
| the control it skips | **7.39** |

Sampling exists so that the record you throw away is cheap, and it is: **34 %
cheaper**, and the saving (6.77 ns) is the downstream `Write` it did not make
(7.39 ns). The decision itself — one `atomic.Uint64.Add` and one modulo — costs
5.9 ns and is paid on every record either way. So at 1-in-100, a caller pays
~5.9 ns × 100 to save ~7.4 ns × 99 against a discard sink (a loss), and
~5.9 ns × 100 to save ~5 500 ns × 99 against the syslog sink (a rout).
**Sampling pays for itself in proportion to how expensive the sink is** — it is
not a free win in front of a cheap one.

### `multi` vs `tee` — both linear, different constants

| destinations | `multi` | `tee` |
|---|---:|---:|
| 1 | 17.19 | 24.04 |
| 2 | 25.59 | 31.53 |
| 4 | 42.66 | 47.72 |
| 8 | 76.71 | — |
| **per extra destination** | **8.4 ns** | **7.9 ns** |
| **fixed overhead** | **8.8 ns** | **16.1 ns** |

**Yes, both are linear** — `multi` from 1 to 8 fits ŷ = 8.8 + 8.4n to within
1 ns at every point. (Before the §0 fix it was not: 19.4 / 28.2 / 69.9 / 84.8 /
156.5, with the 3-branch step 2.5× the 2-branch one.) `tee` costs ~7 ns more at
the fixed end because it tracks acceptance and holds the spill seam; its
marginal cost is fractionally lower. Neither allocates at any width.

### `recover` — the classic hidden cost, priced

**+20.5 ns per record**, always, whether or not anything panics — the deferred
closure is set up and torn down on every `Write`. That is 2.8× the whole
control sink, and more than `multi` fanning out to two destinations. It is not
free, and it is not expensive; against a `file` sink at 1 013 ns it is **2 %**,
against the discard control it is **277 %**. Wire it where a sink might
genuinely panic (third-party code, per `middleware/CLAUDE.md`), not by reflex.

### `failover` — the happy path is now genuinely free of the chain

| | ns/op |
|---|---:|
| 2-branch chain, first accepts | 16.80 |
| **5-branch chain, first accepts** | **17.18** |
| 2-branch chain, first FAILS, second accepts | 28.70 |

The happy path is **+9.4 ns** over the control and — since §0 — independent of
how long the chain is. Failing over costs **+11.9 ns** on top, which is the
second branch's call plus one `append` to a nil slice. Cheap enough that the
policy costs nothing worth reasoning about; the expensive part of a failover is
the failing sink's own timeout, which is not measurable here and never will be.

### `route` — a predicate is ~5.5 ns

First-match 19.95 ns, fourth-match 36.47 ns → **+5.5 ns per rejected
predicate** for `LevelAtLeast` (a closure call and one comparison). The
fallback path (20.25 ns) costs the same as a first match, so a table that
mostly misses is not penalised for missing. A router with a long table is
priced by its table length; put the common route first.

### `async` — the publisher's cost, and a warning

**The consumer's work is not in these numbers.** `async` moves the downstream
`Write` to a drainer goroutine; what the caller pays is the hand-off.

| | ns/op | drops | allocs |
|---|---:|---:|---:|
| paced (ring never saturates) | **195.6** | **0** | 0 |
| unpaced tight producer loop | 162.9 | **6 958 992 of 7 444 324** | 0 |

Two results, and the second is the important one.

**1. The hand-off costs ~196 ns.** That is 26× the control and 9× the console
sink it exists to get off the hot path. A CPU profile says where it goes:

```
    1000ms 10.42%  runtime.futex
     880ms  9.17%  runtime.procyieldAsm
     860ms  8.96%  internal/sync.(*Mutex).Unlock
     440ms  4.58%  internal/sync.(*Mutex).lockSlow
```

~33 % is the `ringMu` handshake with the drainer. The package comment argues
"the atomic operations inside are cheap enough that wrapping them loses little"
— **the measurement does not support that**: the mutex turns a lock-free ring
push into a two-goroutine futex negotiation. The mutex is there for a stated
correctness reason (the SPSC ring has two producers in practice) and this is
not an argument to remove it; it is the price tag that was missing.

The operational consequence: **`async` in front of `console` (21 ns) or `file`
(1 013 ns) is close to a pessimisation for the console case.** It pays for
itself against anything slower than ~200 ns per record — which means syslog
over the network (5 400 ns) and remote drains, exactly the use case
`middleware/CLAUDE.md` names. In front of a local file it buys latency
*variance* (fsync spikes never reach the caller), not throughput.

**2. Under an unpaced producer the ring collapses.** A caller emitting in a
tight loop drops **93 % of its records** with the default `DropNewest` policy,
even with a 65 536-slot ring, because the producer re-acquires `ringMu`
immediately while the drainer must go through a downstream `Write` and a
channel select. The producer wins the mutex nearly every time and starves its
own consumer. **A 65 536-slot ring did not help**, because the problem is not
capacity — it is that the drainer never gets scheduled. `OnDrop` is the only
way anyone would ever find out; wire it.

### `encwrite` — 1 318 ns, 6 allocations, and the cause is not in this package

> **Superseded 2026-11-11 (#813)**: 0 allocations and 338 ns — see
> §`encwrite` after #813 at the end. The analysis below is the before.

The most expensive middleware by two orders of magnitude. `-memprofile`:

```
   1936397 17.90%  crypto/aead/aesgcm.aesGCM.Seal  .../aesgcm/aesgcm.go:74
   1850498 17.11%  encwrite.frame                  .../encwrite/encwrite.go:174
   1842500 17.03%  crypto/internal/fips140/aes/gcm.New       (inline)
   1835036 16.96%  crypto/aead/aesgcm.aesGCM.Seal  .../aesgcm/aesgcm.go:62
   1741650 16.10%  crypto/internal/fips140/aes.New           (inline)
   1572911 14.54%  bytes.Clone
```

**Five of the six allocations are inside `internal/service/crypto/aead/aesgcm`, and
the dominant one is that `aes.NewCipher` + `cipher.NewGCM` run on EVERY
record** — a full AES key schedule expansion per log line, plus a `bytes.Clone`
of the key. A cached `cipher.AEAD` held beside the subkey would remove four of
the six and most of the 1 318 ns. That package is outside this group; the
finding is recorded here with the profile lines because this benchmark is where
it surfaced.

The one allocation that *is* ours — `encwrite.frame`, the 4-byte length prefix
— is 1 of 6 and roughly 40 ns of 1 318. **Not worth optimising** while the
other five stand; pooling it would complicate a lifetime for 3 % of the cost.

---

## 2. The four terminal sinks

| sink | ns/op | B/op | allocs | what dominates |
|---|---:|---:|---:|---|
| `discardSink` (control) | 7.39 | 0 | 0 | one interface call |
| `console` → `io.Discard` | 21.43 | 0 | 0 | mutex + ctx check |
| `syslog` — framing only | 142.6 | 192 | 1 | the RFC5424 frame buffer |
| `memory` | 273.9 | 487 | 1 | the deep attribute clone |
| `file` → real file, 0700 dir | 1 060 | 0 | 0 | **the `write(2)` syscall** |
| `syslog` → UDP loopback | 5 322 | 244 | 3 | **the datagram** |

### `console` — +14 ns, and that is the whole story

`sync.Mutex` Lock/Unlock plus a `ctx.Err()` check plus the `io.Writer` call.
**Nothing to optimise.** The mutex is the atomic-line contract the sink
documents; 14 ns is a fair price for it and there is no cheaper mechanism that
still serialises.

### `file` — the write dominates, 99 % of it

1 060 ns against console's 21 ns over the same mutex-and-check preamble:
**~1 039 ns (98 %) is `os.File.Write`** — one `write(2)` per record into the
page cache. Zero allocations. The SDK's contribution to a file write is 2 % of
its cost. **Nothing to optimise in the sink**; the only lever is doing fewer,
larger writes, which means buffering, which the sink layer deliberately refuses
(`sink/CLAUDE.md`: "Do NOT add buffering inside a terminal sink — that is
`async`'s job"). Note the run-to-run spread here is the widest in the report
(1 013 / 1 060 / 1 450 ns) because it is real I/O on a shared VM.

### `memory` — 1 alloc/op is the point of the sink, not a defect

274 ns and 487 B per write, all of it `deepCloneAttrs`. The memory sink retains
the **record**, not the bytes, and deep-clones the attribute slice (including
nested groups) so caller mutation cannot corrupt recorded history — a
deliberate V37 decision. It is a test sink and this is what a faithful test
sink costs. **Nothing to optimise.**

One caveat the benchmark had to work around and callers cannot: the sink is
**unbounded**. Measured naïvely it reported 604 ns / 814 B, because the run was
measuring an ever-growing slice being memmoved, not a write. The published
number resets the buffer outside the timer every 1 024 writes. A long-running
process that wires `memory` will grow until it dies.

### `syslog` — the transport is 37× the formatting

| | ns/op | allocs |
|---|---:|---:|
| framing only (no-op conn via `Config.Dialer`) | **142.6** | 1 (192 B) |
| framing + real UDP datagram to loopback | **5 322** | 2–3 (244 B) |
| **the transport** | **~5 180 (97 %)** | +1–2 |

The message **formatting** — `makeFrame` building `<PRI>1 - - - - - - <payload>`
— is 142.6 ns and one 192-byte allocation, and that allocation is inherent:
the frame is a new buffer the sink hands to the socket. It could be pooled; at
2.7 % of the real cost, **it is not worth optimising.**

The transport was measured honestly rather than skipped: `syslog.Config.Dialer`
is the package's own documented seam, so the framing-only line injects a
counting no-op `net.Conn` (faking the *connection*, which the package invites),
while the loopback line dials a real `net.ListenPacket("udp", "127.0.0.1:0")`
with a goroutine draining it so the kernel receive buffer cannot fill and
silently start discarding.

**What is genuinely unmeasurable here, and why it is not the container's
fault:** this container *does* have a syslog socket — `/dev/log` →
`/run/systemd/journal/dev-log`, a unix datagram socket. The SDK's syslog sink
**cannot use it**: `NewWithConfig` refuses any network other than `udp` or
`tcp` (`ProtoInvalid`). So "ship to the local syslog daemon", the most common
syslog deployment on Linux, is not a path this sink has — that is a capability
gap, not a measurement gap, and it is why no `/dev/log` number appears above.
The 5 322 ns loopback figure is a real socket and a real syscall and should be
read as a floor: a datagram to another host adds the network.

---

## 3. Where a real emit's time actually goes

CPU profile of `Emit_Depth0` — the full `Build().Send()` — taken *before* the
encoder work in `encoder/BENCH.md`, at 1 202 ns:

| | cum % |
|---|---:|
| `encoder.(*textEncoder).Append` | **46.1** |
| — of which `time.Time.AppendFormat` | 34.6 |
| — of which attribute rendering | 6.2 |
| `runtime.Callers` (caller-PC capture in `Send`) | **22.5** |
| `clock.Now` | 7.2 |
| `mergeAttrs` (**the one allocation**) | 6.2 |
| the sink | ~0.6 |

Two things a reader should take from this. First, **the sink chain is noise**
in a realistic emit: the control sink is 7.4 ns of 853, and even four
middlewares add 60 ns to a ~900 ns call. The middleware numbers in §1 matter
for their *allocation* behaviour (§0) far more than for their nanoseconds.

Second, **the encoder was half the emit** and the timestamp was three quarters
of that. Fixing it (`encoder/BENCH.md` §5.2) is why every line in §0's "after"
column dropped ~340 ns. `runtime.Callers` at 22.5 % is now proportionally the
largest single item in an emit; it belongs to the core, not to this group, and
is recorded here for whoever measures that.

---

## 4. Summary — what is and is not worth optimising

**Fixed** (both profile-designated, both with before/after above):
1. `multi.Write` / `failover.Write` per-record allocation — restored the
   one-allocation-per-emit claim at any fan-out width, 2–3.9× on the affected
   lines.
2. (In `encoder/`) `strconv.AppendQuote` and `time.Time.AppendFormat` — 28 %
   off every emit in this report.

**Measured and deliberately left alone:**
- `console` (+14 ns): the mutex is the contract.
- `file` (1 060 ns): 98 % is `write(2)`; buffering is `async`'s job by design.
- `memory` (274 ns, 1 alloc): the clone is the sink's purpose.
- `syslog` framing (143 ns, 1 alloc): 2.7 % of the real cost.
- `encwrite.frame` (1 of 6 allocs): 3 % of a cost owned by another package.
- `route`, `sample`, `recover`, `tee`: all 0 allocs, all under 50 ns, all
  linear or constant. Nothing to reclaim.

**Reported to other owners:**
- `internal/service/crypto/aead/aesgcm.Seal` builds a fresh AES cipher and GCM AEAD
  **per call** — 5 of `encwrite`'s 6 allocations and most of its 1 318 ns.
- `async`'s `ringMu` costs ~33 % of a 196 ns hand-off, and an unpaced producer
  starves the drainer into dropping 93 % of records.
- `runtime.Callers` is 22.5 % of an emit (core, not this group).
- Both encoders render an `error` attribute as `?` (see `encoder/BENCH.md` §2).

## 5. The drainer parks, a backlog is one write, a Flush holds no writer (#806, #807, #815)

Taken on a different machine from the sections above — AMD EPYC 7351P, 4 Ps
(`GOMAXPROCS=4`), linux/amd64, go1.27.3, ext4 on a shared VM disk, other
agents compiling — `-benchtime=100ms` to `300ms`, 3 to 4 rounds. The base is
`origin/main` 740e7c1e in a second worktree, run alternately with this branch.

**An idle async sink costs no CPU (#806).** The drainer parked on `Gosched`
when its ring was empty and, with nothing else runnable, came straight back:
`TestAnIdleAsyncSinkCostsNoCPU` measured **690 ms of process CPU over an idle
500 ms on the base** — one P flat out — and under 25 ms (the test's bound) on
this branch, where the drainer parks on a wake-up the next accepted `Write`
sends (the pending-flag-and-recheck shape `net/loop`'s post queue uses).

```
                                        base        this branch
MW_Async_PublishPaced    ns/op        182–196        154–167
MW_Async_PublishSaturated drop ratio  83–90 %        91–93 %
```

The paced hand-off is ~15 % cheaper: the producer no longer competes for
`ringMu` with a drainer polling an empty ring. **The unpaced producer still
starves its drainer**, within noise of the base: that collapse is the
`ringMu` handshake (§1 `async`), which parking does not change. The MPSC ring
#805 proposed for it (`ring.NewMulti`, ADR 0216) was measured and is not
adopted: four producers saturating it take ~417 ns a record against ~235
behind `ringMu` (`internal/kernel/collections/ring/BENCH.md`), and a writer
of it may not read, so `DropOldest` — evict the oldest record and store the
new one under one lock, the newest always admitted — would become the
drainer's, and a writer facing a full ring would wait for it or lose its own
record.

**A backlog is one write(2) (#807).** `Sink_File_Batch16` is one `WriteBatch`
of 16 records against `Sink_File`'s one `Write` per record:

```
Sink_File            1059–1150 ns/op per record     0 allocs
Sink_File_Batch16     228–582  ns/record (16/op)    0 allocs
```

The minimum meets the issue's 300 ns per record; the spread is the shared
disk. The async drainer reaches it only with `Config.BatchMax` above one and
a downstream that implements `BatchSink` (file, rotfile, the level gate, the
tcp writer); the zero Config delivers record by record, as before.

The udp writer and journald joined them later the same day: a backlog of 16
leaves in one `sendmmsg(2)` on Linux, each record still its own datagram —
3.0–3.1 µs per record against 3.3 µs for one `sendto(2)` each on this VM, the
kernel's per-datagram work being most of it (`internal/datagram/BENCH.md`).
The http writer posts one NDJSON body per backlog only under
`NetConfig.HTTPBatchNDJSON`, the opt-in its wire change needs.

**A Flush no longer holds the writers (#815).** `Sink_File_WriteDuringFlush`
runs parallel writers while one goroutine flushes in a loop:

```
                           base               this branch
ns/op (4 writers)        22 786–69 197         1 742–1 779
max-ns/write             6.1–29.8 ms           3.3–8.6 ms
```

On the base every writer waited for the fsync under the write mutex; now the
throughput is a write's own. The worst write is still milliseconds: ext4
itself serialises a `write(2)` behind a journal commit an fsync started, which
no lock in this package can remove.

## 6. Bound attributes rendered once (#1016)

Same machine and method as §5. `Emit_Bound` is one static emit through a
Logger bound (`With`) with 0, 8 and 32 short string attributes; the base renders
the bound set with the record's on every emit, this branch renders it once at
`With` (`corelogger.PrefixEncoder`) and splices the bytes.

```
                      base (ns/op)       this branch (ns/op)    allocs
text  0 bound          502 – 569           294 – 317              0
text  8 bound         1233 – 1321          539 – 574              1
text 32 bound         3037 – 3638         1054 – 1164             1
json  0 bound          530 – 602           313 – 325              0
json  8 bound         1280 – 1445          584 – 652              1
json 32 bound         2975 – 3546         1110 – 1373             1
```

(The 0-bound rows moved too: they include #1014's removal of the stack walk.)
**The rendering is gone; the growth that is left is the merged slice.** A
bound attribute cost ~78 ns per emit on the base and costs ~25 ns now — what
remains is `mergeAttrs`, the copy of the bound attributes into the record the
SINK receives (route predicates and the memory sink read them). So the
issue's bound — the 8- and 32-bound rows within 10 % of the 0-bound one — was
**not met** by these figures: 8 bound is ~1.8x, 32 bound ~3.6x the 0-bound
row. The bound is the issue's, unchanged, and #1016 stays open on it.

**The copy is gone too (#1016, second change).** A record with no attribute of
its own — the rows above — now carries the bound array itself, capped at its
length (`h.attrs[:n:n]`): the array is immutable once bound and the record is
read-only for the sink, so a sink's `append` reallocates and never writes into
it (`TestASinkAppendingToTheSharedBoundAttrsLeavesThemIntact`, under the race
detector, fails at "800 of 800 records shared the bound array with room past
its length" and a data race when the cap is dropped). And a sink that reads
only the bytes says so (`corelogger.AttrsRequester`, false on console, file,
syslog, journald, nettransport and rotfile, forwarded by the decorators, ORed
by the fan-outs), so a record WITH attributes of its own is not merged for it
either. What still grows with the bound count is the splice of the prefix
into the line, a `memmove` of its bytes. The rows above are the run before
this change; the runner's interleaved A/B (`bench-ab.yml`, the `logger` suite)
measures this one, and its figures replace them here.

## 7. The keyed sampler (#1017)

Same machine and method as §5, `-benchtime=200ms`, 3 rounds, beside the 1-in-N
sampler measured in the same run:

```
MW_Sample_Kept             20.1 – 20.8 ns/op    0 allocs
MW_Sample_Dropped          12.5 – 13.0 ns/op    0 allocs
MW_SampleKeyed_Kept        99.4 – 105.8 ns/op   0 allocs
MW_SampleKeyed_Dropped    101.6 – 114.0 ns/op   0 allocs
```

The keyed sampler costs ~100 ns a record, kept or dropped: the clock read, a
mutex, and one hash of the record's message (the default key is the level and
the message, as a struct, so nothing is concatenated). That is five to eight
times the global counter's, and the price of a sampler that thins a burst per
message and lets the rare line through — the 1-in-N one drops it 99 % of the
time. Both paths allocate nothing once the key is in the table; the budget
`allocs: 0` on `(*Keyed).Write` (design/sdk.yaml, alloc lane) holds it.

## Reproducibility envelope

> **Numbers vary across machines.** `ns/op` is load-sensitive; `allocs/op` is
> not. Every allocation count in this report was identical across all runs at
> every load level observed, which is why the §0 verdict is stated flatly and
> the nanoseconds are not.

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P 16-Core Processor |
| CPU cores (guest)  | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | 1da8e4c |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-benchtime=1s -count=3`; median published |
| 1-min load average | before-run 0.70, after-run 2.61 — both under this 8-core box's ~4 noise floor |

The control (`Sink_Discard`) measured 7.367 / 7.435 / 7.373 ns in the before
run and 7.386 / 7.368 / 7.430 ns in the after run — 0.9 % apart across a
half-hour and a 3.7× change in load average. That agreement is what licenses
comparing the two columns of §0 directly. A third confirmation run of the four
load-bearing lines, taken later at load 2.13, reproduced them within 1.1 %
(control 7.388, `MW_Multi_3` 34.35 ns / 0 allocs, `Emit_Depth0` 845.7 ns /
1 alloc, `Emit_FanOut4` 912.3 ns / 1 alloc). Spread was under 2 % on every
middleware line; the exceptions are `Sink_File` (1 013–1 450 ns, real I/O) and
`Sink_Syslog_UDPLoopback` (5 300–5 507 ns, real sockets), both flagged in place.

`async` benchmarks are published from a race-detector-OFF run; they were
additionally run under `-race` for correctness only, and those timings are not
reported.

## Results

`BenchmarkBuild_ZeroAlloc` is the pre-existing core benchmark this file already
carried; it writes into a `bytes.Buffer` through a `console` sink, so its B/op
includes that buffer's amortised growth and it is not comparable to the
`Emit_*` lines above (which target the discard control). It is reported from
its own run at the same load, and it is the line that carries the historical
comparison: **562.1 ns before this branch on a 12-core i7-1255U, 882.3 ns here
on an 8-core EPYC 7351P** — a slower box, not a regression, and the reason this
report does not compare across machines.

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/observe/logger
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkBuild_ZeroAlloc-8                 	 1400779	       882.3 ns/op	     319 B/op	       1 allocs/op
BenchmarkSink_Discard-8                    	162818996	         7.368 ns/op	       0 B/op	       0 allocs/op
BenchmarkSink_Console_Discard-8            	56245368	        21.43 ns/op	       0 B/op	       0 allocs/op
BenchmarkSink_Memory-8                     	 4505714	       273.9 ns/op	     487 B/op	       1 allocs/op
BenchmarkSink_File-8                       	 1000000	      1060 ns/op	       0 B/op	       0 allocs/op
BenchmarkSink_Syslog_FramingOnly-8         	 8773009	       142.6 ns/op	     192 B/op	       1 allocs/op
BenchmarkSink_Syslog_UDPLoopback-8         	  239594	      5322 ns/op	     244 B/op	       3 allocs/op
BenchmarkMW_Route_FirstMatch-8             	59996106	        19.94 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Route_FourthMatch-8            	33208021	        36.47 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Route_Fallback-8               	60151002	        20.25 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Sample_Kept-8                  	59884249	        20.04 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Sample_Dropped-8               	90294996	        13.27 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Multi_1-8                      	67722879	        17.19 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Multi_2-8                      	46910301	        25.59 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Multi_3-8                      	33532519	        34.53 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Multi_4-8                      	26566300	        42.66 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Multi_8-8                      	15794860	        76.71 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Tee_1-8                        	48541303	        24.04 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Tee_2-8                        	37470416	        31.53 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Tee_4-8                        	25539544	        47.72 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Recover-8                      	43477477	        27.90 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Failover_FirstOK-8             	71470006	        16.80 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Failover_SecondOK-8            	41859817	        28.70 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Failover_FirstOK_5Branches-8   	69178754	        17.18 ns/op	       0 B/op	       0 allocs/op
BenchmarkMW_Async_PublishSaturated-8       	 7444324	       162.9 ns/op	   6958992 drops	       2 B/op	       0 allocs/op
BenchmarkMW_Async_PublishPaced-8           	 5985262	       195.6 ns/op	         0 drops	       0 B/op	       0 allocs/op
BenchmarkMW_EncWrite-8                     	  955256	      1318 ns/op	    1728 B/op	       6 allocs/op
BenchmarkEmit_Depth0-8                     	 1409060	       854.2 ns/op	     128 B/op	       1 allocs/op
BenchmarkEmit_Depth1-8                     	 1396378	       859.4 ns/op	     128 B/op	       1 allocs/op
BenchmarkEmit_Depth2-8                     	 1296219	       934.9 ns/op	     128 B/op	       1 allocs/op
BenchmarkEmit_Depth4-8                     	 1312017	       912.9 ns/op	     128 B/op	       1 allocs/op
BenchmarkEmit_FanOut2-8                    	 1304528	       914.5 ns/op	     128 B/op	       1 allocs/op
BenchmarkEmit_FanOut4-8                    	 1316478	       916.7 ns/op	     128 B/op	       1 allocs/op
BenchmarkEmit_Depth4_AllocFree-8           	 1314613	       909.6 ns/op	     128 B/op	       1 allocs/op
```

## `encwrite` after #813 (2026-11-11)

AMD EPYC 7351P VM (4 CPUs of the lane), Linux 6.12, go1.27.3, branch
`wip/sdk-lot-4-suite`, before (`b5285df65`) and after on the same box,
`-benchmem -count 8`, benchstat:

| | sec/op | B/op | allocs/op |
|---|---|---|---|
| `MW_EncWrite` before | 1 163 ns ± 3 % | 1 696 | 5 |
| `MW_EncWrite` after | **337.6 ns ± 2 %** (−71 %, p = 0.000) | 0 | **0** |

(5, not the 6 above: `bytes.Clone` of the key had already gone from `Key.Bytes`.)

- **The cipher is built once.** `crypto.NewSealer` binds the subkey to
  AES-256-GCM at construction (`aesgcm`'s `Bind`): no `aes.NewCipher`, no
  `cipher.NewGCM`, no key copy per record — four of the five.
- **The box is sealed straight after its prefix.** `Sealer.SealTo` appends
  `[version][id][nonce][ciphertext||tag]` to a frame borrowed with
  `buffer.GetSize` and given back once the downstream's `Write` has returned
  — a sink never keeps `p`, since the handler recycles its own buffer the
  same way. The fifth allocation, `frame`'s copy, is gone.
- **The nonce is drawn into its place in the box**: a `[12]byte` of its own
  escaped through the `cipher.AEAD` interface call, 16 B a box.

The budget `allocs: 0` on `(*EncWriter).Write` (`design/sdk.yaml`, fixture
`perfWrite`) holds it in the alloc lane. Command:
`cd internal && go test -run '^$' -bench 'MW_EncWrite$' -benchmem -count 8 ./service/observe/logger/`.
