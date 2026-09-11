<!-- generated from internal/service/writer/levelgate/gate_bench_test.go — run `go test -run='^$' -bench=. -benchmem -benchtime=1s -count=5 ./internal/service/writer/levelgate/` and take medians -->
# Benchmarks — `internal/service/writer/levelgate`

This gate runs on **every record** a writer is handed, including every one it
discards. Its cost is therefore paid by exactly the callers who configured it so
that work would not happen, which is why the headline here is the DROP and not
the pass: a consumer who sets `MinLevel: Error` to make debug logging free is
buying the difference between two numbers, and until now nobody had measured
either of them.

> **Every figure below is the median of five runs** at `-benchtime=1s`. Spreads
> were under 3 % on every row except `WriteParallelRetuned/atomic` (19.5 %,
> diagnosed in §3). The allocation columns are exact and did not vary.
> Load average at the start of the sweep: 0.23.

## 1. A dropped record costs 6.84 ns and allocates nothing

| one `Write`, 83-byte payload | ns/op | B/op | allocs |
|---|---:|---:|---:|
| **no gate at all** — what `New(inner, Info)` returns | 5.911 | 0 | 0 |
| **dropped** — `min=Error`, record at `Info` | **6.862** | **0** | **0** |
| passed — `min=Warn`, record at `Error` | 13.54 | 0 | 0 |
| the same delegation with the comparison REMOVED | 13.58 | 0 | 0 |

Medians of **nine samples across three separate processes**. An earlier pass
took three samples in one process and published 5.95 / 6.84 / 13.58 / 13.53;
those are within the spread of these and are superseded rather than
contradicted, but see the two paragraphs below for what the tighter protocol
changed about how they may be READ.

Three things follow, and the third is the one that was worth measuring.

**The drop costs about a nanosecond over a sink with no gate in front of it**
— 0.95 ns by these medians — and in exchange it removes the entire downstream
write, which is 302 ns even when the destination is `/dev/null` and 1 868 ns
when it is a container's stderr (`../console/BENCH.md` §1). A gate installed to
suppress debug logging returns its own cost **more than three hundred times
over** on the first record it discards, at the cheapest destination this SDK
has.

The second digit is deliberately not quoted. The ungated arm's own spread across
these nine samples is 5.867 to 6.398 — **0.53 ns, more than half the quantity
being measured** — so "0.89" and "0.95" are the same reading taken twice, and
neither supports two significant figures. The conclusion never rested on them:
one nanosecond against three hundred is the same decision at either value.

**The comparison itself is not measurable, and its sign is not stable.**
Three samples in one process put `passed` 0.05 ns ABOVE the same sink with
`if r.Level < s.min` deleted; nine samples across three processes put it
0.04 ns BELOW — the gated path measuring marginally faster than the ungated
one, which cannot be a real effect and is therefore proof the difference is
noise. It is reported here as unmeasurable rather than as a small number with a
direction, because a signed figure invites a reader to trust the sign. The
7.6 ns a passing record pays over an ungated one is one extra interface hop
carrying a 104-byte record by value — not the branch.

**Nothing on either path reaches the heap.** An allocation profile taken at
`-memprofilerate=1` over **529 020 912** dropped records — every allocation
sampled, none missed — accounts for 1 271 objects in the whole process, and
`gateSink.Write` is not among them:

```
Showing nodes accounting for 721, 56.73% of 1271 total
Showing top 8 nodes out of 126
      flat  flat%   sum%        cum   cum%
       153 12.04% 12.04%        153 12.04%  regexp/syntax.(*compiler).inst (inline)
       151 11.88% 23.92%        151 11.88%  runtime/pprof.allFrames
        93  7.32% 31.24%         93  7.32%  regexp/syntax.(*parser).newRegexp (inline)
        86  6.77% 38.00%        100  7.87%  runtime/pprof.(*profileBuilder).emitLocation
        75  5.90% 43.90%         75  5.90%  regexp/syntax.(*parser).maybeConcat
        62  4.88% 48.78%        137 10.78%  regexp/syntax.(*parser).push
        53  4.17% 52.95%        565 44.45%  regexp.compile
        48  3.78% 56.73%         54  4.25%  runtime.mallocgc
```

Every one of those is the harness: pprof building its own profile, and
`regexp` compiling the `-bench` pattern. The CPU profile of the same run is the
whole package:

```
Duration: 4.31s, Total samples = 4.29s (99.60%)
      flat  flat%   sum%        cum   cum%
     3.02s 70.40% 70.40%      4.28s 99.77%  …/levelgate.serialRow.func1
     1.26s 29.37% 99.77%      1.26s 29.37%  …/levelgate.(*gateSink).Write
```

`TestGateAllocatesNothingInEitherDirection` now pins both directions and is
mutation-checked in its own doc comment.

## 2. 43 % of a dropped record is copying a record the gate reads one byte of

`core/logger.Sink` takes `RecordEvent` **by value**, and the struct is 104 bytes
(`Time` 24, `Level` 1 + 7 padding, `Message` 16, `PC` 8, `Attrs` 24,
`TraceContext` 24 — verified with `unsafe.Sizeof`). The gate reads exactly one
field of it, `Level`, which is one byte.

| the same drop, one interface call | ns/op |
|---|---:|
| record by value (104 B) — **what ships** | 6.87 |
| the identical branch with the record by reference | **4.22** |

**2.65 ns per hop, 39 % of the drop.** The arithmetic closes against §1: a
passing record makes two hops and pays 13.58 ns, where an ungated one makes one
and pays 5.95 ns — the extra hop costs 7.63 ns, of which 2.65 ns is this copy
and the rest is the call. The by-value row also agrees with §1's `dropped` row
to 0.5 % (6.87 against 6.84), which is the same code measured through two
different benchmark functions.

**TESTED AND REFUSED.** The port is not widened. It is frozen in `core/logger`,
which ADR 0039 forbids extending in place, and the by-value record is also what
stops a sink mutating a record its siblings in a `multi` fan-out have not seen
yet. Three nanoseconds is the price of that, stated rather than assumed.

The by-reference row reads its record from the heap (taking `&rec` outside the
loop makes it escape), so if anything it understates its own advantage.

## 3. The floor does not move, and that is worth 45×

`level.Var`'s doc comment describes exactly this package:

> a gate may read Level on the hot path while a control goroutine raises or
> lowers the floor via `Set`.

**No gate in this repository does that.** `level.Leveler` and `level.Var` are
published through `pkg/v1/logger` as aliases and consulted by nothing on any hot
path; `levelgate` freezes its floor at construction, so retuning it means
rebuilding the writer. That is a defensible design, and these are the numbers
that make it one rather than an omission.

| 8 goroutines on one gate, dropped record | ns/op | vs shipped |
|---|---:|---:|
| **immutable field — what ships** | **0.931** | 1.00× |
| the same load through an atomic, held concretely | 0.943 | 1.01× |
| the same atomic read through the `level.Leveler` port | 1.112 | 1.19× |
| an `RWMutex` read on every record | 40.52 | **43.5×** |

| the same four, uncontended | ns/op | vs shipped |
|---|---:|---:|
| immutable field | 6.89 | 1.00× |
| atomic, held concretely | 7.12 | 1.03× |
| atomic through `level.Leveler` | 8.48 | 1.23× |
| `RWMutex` | 21.93 | 3.18× |

Two conclusions, and the first is the one the usual framing gets wrong.

**The atomic is free; the PORT is not.** An `atomic.Int32.Load` on amd64 is a
plain `MOV`, and the concretely-held variant sits within 3 % of an immutable
field serially (7.12 vs 6.89) and within 1.3 % at eight-way parallelism (0.943
vs 0.931) — inside this machine's own run-to-run variation, and on an earlier
sweep the atomic measured marginally FASTER. The 1.19–1.23× that
`atomic_leveler` costs is the second interface call `Leveler` interposes, not
the atomic behind it. "Atomic versus mutex" is the wrong question here; "one
indirection or none" is the right one.

**The mutex does not merely cost more, it stops scaling.** The shipped gate at
0.931 ns/op across eight goroutines is 7.45 ns of goroutine time against 6.89 ns
measured serially — **linear to within 9 %**, which is what having no shared
mutable state buys. The `RWMutex` variant is 43.5× worse at the same width while
being only 3.18× worse alone.

And with a control goroutine genuinely moving the floor:

| 8 readers + 1 writer | ns/op | vs shipped, quiescent |
|---|---:|---:|
| atomic | 3.43 | 3.7× |
| `RWMutex` | 149.10 | **160×** |

The atomic row's spread is 19.5 % across five runs (2.92–3.59 ns) because it is
measuring how often the control goroutine's store invalidates the readers' cache
line, which the scheduler decides; it is published with that caveat rather than
as a precise figure. The mutex row's 149.10 ns is not ambiguous.

**TESTED AND REFUSED.** A dynamic floor is not added. It would cost 1.19–1.23×
per record through the published `Leveler` port for a capability nothing asks for,
and `New` already answers the reconfiguration case: the floor is a `writer`
config field, and changing it is `writer.Open` again. What this section fixes is
`level.Var`'s doc comment, which describes a consumer that does not exist.

## 4. Construction

| `New(inner, min)` | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `min == Info` — the "inherit" sentinel | 3.07 | 0 | 0 |
| `min == Error` — a real floor | 29.99 | 24 | 1 |

The sentinel allocates nothing: `New` returns `inner` untouched, so a writer at
the default level has no wrapper to call and pays the 5.95 ns row of §1, not the
6.84 ns one. The 24 bytes of the wrapped case are the
`gateSink` itself — a 16-byte `Sink` interface plus a padded `int8` — and the
figure reappears exactly as the delta between the two `writer.Open` rows in
`../console/BENCH.md` §4, which is an independent path to the same number.

## 5. Rows thrown away

- **A first allocation guard passed against a mutation that really did
  allocate.** `testing.AllocsPerRun` ends in `float64(mallocs / uint64(runs))` —
  an integer division, documented in the stdlib as being there so a caller can
  write `== 1` instead of `< 2`. Drop accounting added to the gate
  (`s.dropped = append(s.dropped, r.Level)`) allocates on the slice's doubling
  steps: six allocations across 500 drops, which that division reports as
  **0.0**. The guard now totals the malloc counter instead of averaging it, and
  the same mutation fails at `500 dropped records performed 6 allocations,
  want 0`.
- **That replacement was itself wrong on its first draft**, for a reason worth
  recording: it called `runtime.GC()` before opening the measurement window, and
  an explicit collection returns before its sweep is finished, so the residual
  work allocated INSIDE the window. It reported exactly one stray allocation in
  10 of 12 runs, and the allocation profile put it on the `runtime.GC()` line.
  `testing.AllocsPerRun` does not call GC either, and this is why.
- **A whole sweep, twice.** The first was taken before the benchmark bodies were
  brought past `ktn-linter` (the row closures now take their parameters instead
  of reading a range variable, and every `Write` error is checked instead of
  discarded), and it read `dropped` at 7.08 ns against 6.84 ns for the code that
  actually ships. Publishing the first would have described a program not in the
  tree, so every number here was re-measured against the committed source — and
  so were both pprof quotes, which is why the CPU profile names `serialRow.func1`
  rather than the anonymous closure the first one showed.
- **A load-dependent 2 % gap.** An intermediate `WriteDynamic` sweep at load
  average 1.34 read 7.19 ns for the immutable gate against 7.04 ns for the same
  code minutes earlier at load 0.11. That is the machine, and it is why every
  published figure is a median of five and the load is disclosed.

## Reproducibility envelope

> **Numbers vary across machines.** The allocation columns are exact. What this
> report asserts is the RATIOS — dropped against ungated, immutable against
> mutable, by-value against by-reference — and one absolute figure a consumer
> sizes with: a dropped record costs 6.84 ns and zero allocations.
>
> The `mutex` rows are the most machine-dependent here. This is a Zen 1 part,
> where a LOCK-prefixed instruction is expensive: `../console/BENCH.md` §2
> measures an uncontended `sync.Mutex` Lock/Unlock pair at 19.63 ns on this
> machine. On a part with cheaper atomics the 43.5× of §3 would shrink; it would
> not invert, because the scaling difference is structural.

| | |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor |
| CPU cores | 8 |
| RAM | 15 GiB |
| OS / kernel | Debian GNU/Linux 13 (trixie), Linux 6.12.101+deb13-amd64 |
| Architecture | amd64 |
| Go toolchain | go1.27.1 linux/amd64 |
| Git branch | jaimerias-que-tu-te-connect |
| Git commit | 35edd8c |
| Generated | 2026-09-10 |
| Bench wall-clock | `-benchtime=1s`, `-count=5`, medians |
| Load average at start | 0.23 |
