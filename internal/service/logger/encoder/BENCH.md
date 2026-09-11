<!-- generated from internal/service/logger/encoder/encoder_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./logger/encoder/` to refresh -->
# Benchmarks — `internal/service/logger/encoder`

The question this file answers: **"I picked an encoder. What does it cost per
record, and what makes that number move?"**

Neither encoder had ever been measured. Both are allocation-free on every line
below — the encoders write into the caller's buffer and keep that promise, at
every attribute count and every type. What they were not was *fast*: two CPU
profiles named two calls that between them were **83 % of a text encode**, and
fixing both made the text encoder **4.5× faster at four attributes and 5.4× at
sixteen**, with byte-for-byte identical output.

## 1. text vs json — and the answer flipped

| attrs | text (ns/op) | json (ns/op) | winner |
|---|---:|---:|---|
| 0 | **136.3** | 275.3 | text, 2.0× |
| 4 | **366.8** | 882.7 | text, 2.4× |
| 16 | **989.3** | 2 649 | text, 2.7× |

All six lines are **0 B/op, 0 allocs/op**.

Before the two fixes below, *json was faster than text* — 1 163 ns against
1 654 ns at four attributes. The heavier format beat the lighter one because
the lighter one delegated its escaping to `strconv` while json hand-rolled its
own. The measurement is what surfaced that; nothing in the code looked wrong.

The per-attribute slope is what a caller sizing a record should use:

| | 0→4 attrs | 4→16 attrs | per attr |
|---|---:|---:|---:|
| text | +230.5 ns | +622.5 ns | ~55 ns |
| json | +607.4 ns | +1 766 ns | ~150 ns |

json's fixed overhead is 2× text's and its per-attribute cost is **2.7×**,
because every key is quoted and escaped and every member carries `","` framing.
Structured logging is not free; this is the number.

## 2. What a caller varies — attribute TYPE

Four attributes of one type, so the delta against a four-string record (366.8 /
882.7) is the type itself.

| type | text | json | note |
|---|---:|---:|---|
| `KindAny` (an **error**) | **201.7** | 397.3 | cheapest — and see below |
| `KindGroup` (nested) | **200.3** | 397.9 | identical to KindAny, for the same reason |
| `KindInt64` | 270.3 | 431.1 | |
| `KindString` (26 chars) | 366.8 | 882.7 | |
| `KindDuration` | 393.4 | 620.3 | |
| `KindFloat64` | 494.1 | 706.6 | shortest-round-trip `'g'` is not cheap |
| `KindTime` | 411.0 | 595.3 | |

**The two cheapest types are cheap because neither encoder renders them.**
`KindAny` and `KindGroup` both degrade to `?` (text) and `"?"` (json) — a
single byte. So an `err` attribute, which is one of the two or three things
anyone actually logs, reaches the output as a question mark. That is the
documented contract (`encoder/CLAUDE.md`, "every other `Kind` degrades to `?`")
and this table is what it costs and what it buys: 165 ns saved and the error
message destroyed. It is a **correctness gap priced by a benchmark**, not a
performance result.

`KindTime` costs 44 ns more than `KindInt64` in text, which is the whole
timestamp renderer of §4 running a second time for the attribute.

## 3. The escaping trap — real, and now visible

Four string attributes of identical length, differing only in content.

| content | text | json |
|---|---:|---:|
| plain ASCII | **332.3** | 784.6 |
| every 5th byte is `"` | 1 413 | 889.2 |
| every 5th byte is a control char | 1 404 | 895.1 |
| **text penalty** | **4.25×** | 1.14× |

**A caller who logs a quoted string pays 4.25× in the text encoder.** json
barely notices (its escaper was always a byte loop). This is the shape of trap
the brief asked about, and it is worth stating plainly: the text encoder is now
*fast on the common case and no faster than before on the escaping case*. The
1 400 ns is `strconv.AppendQuote` doing exactly the work it always did.

Before the §5.1 fix, both columns cost the same ~1 620 ns, because the plain
string paid the escaping analysis too. The optimisation did not make escaping
cheaper; it stopped charging everyone for it.

## 4. Group prefix and trace context

| | text | json |
|---|---:|---:|
| 4 attrs, no groups (baseline) | 366.8 | 882.7 |
| 4 attrs under `http.request.` | 483.2 | 1 168 |
| 4 attrs + trace context | 439.7 | 1 025 |

- A two-deep group prefix costs **+116 ns** (text) / **+285 ns** (json) — it is
  re-emitted in front of *every* attribute, so the cost is per attribute, not
  per record. Sixteen attributes under two groups would pay it sixteen times.
- **Trace correlation costs +73 ns (text) / +142 ns (json) and zero
  allocations.** `AppendTraceIDHex` hex-encodes into a stack array, exactly as
  `encoder/CLAUDE.md` claims; this is the first measurement of that claim. An
  absent trace context costs nothing at all — the `IsValid` check is one
  comparison and the function returns the buffer untouched.

## 5. The two optimisations, each with the profile line that ordered it

Both were found the same way: `-cpuprofile` on a four-string-attribute encode,
then `go tool pprof -top`. Neither was guessed.

### 5.1 `strconv.AppendQuote` — 70.19 % of a text encode

```
      flat  flat%   sum%        cum   cum%
     0.89s 24.79% 24.79%      2.52s 70.19%  strconv.appendQuotedWith
     0.60s 16.71% 41.50%      0.60s 16.71%  unicode/utf8.DecodeRuneInString (inline)
     0.58s 16.16% 57.66%      0.58s 16.16%  strconv.IsPrint
     0.34s  9.47% 67.13%      1.03s 28.69%  strconv.appendEscapedRune
```

A third of the entire encode was `DecodeRuneInString` + `IsPrint`: a full
Unicode printability analysis of every rune of every value, run to discover
that a log line made of ASCII needs no escaping.

`appendQuotedString` (text.go) scans the bytes first and copies verbatim when
every one of them is printable ASCII other than `"` and `\` — precisely the set
`AppendQuote` passes through unchanged — and hands everything else to
`AppendQuote` untouched. Pinned by `TestAppendQuotedStringMatchesStrconv`,
which sweeps **all 256 byte values in three positions** plus multi-byte runes
and compares the two renderings byte for byte.

| | before | after | |
|---|---:|---:|---:|
| `Text_Attrs4` | 1 654 ns | 642.3 ns | **2.58×** |
| `Text_Attrs16` | 5 290 ns | 1 231 ns | **4.30×** |
| `Text_EscapePlain` | 1 587 ns | 582.0 ns | **2.73×** |
| `Text_EscapeQuoted` | 1 616 ns | 1 641 ns | 0.98× — falls back, as designed |
| `Text_Attrs0` | 388.7 ns | 396.9 ns | unchanged — no strings in the record |

The last two rows are the control: the change moved exactly what it should and
nothing else.

### 5.2 `time.Time.AppendFormat` — 34.58 % of a whole *emit*

Profiling the encoder alone understates this one. On a full
`Build().Send()` through a discard sink — builder, handler, encoder and sink
together — the timestamp was a third of everything:

```
     0.01s  0.27%      1.29s 34.58%  time.Time.AppendFormat
     0.44s 11.80%      1.28s 34.32%  time.Time.appendFormat
     0.37s  9.92%      0.37s  9.92%  time.appendInt
     0.28s  7.51%      0.29s  7.77%  time.nextStdChunk
```

`nextStdChunk` is the generic formatter **re-parsing the layout string** on
every log record, to reach a result whose shape never varies.

`timestamp.go` writes the same bytes at fixed offsets. The comparison now
lives in the tree as `BenchmarkAppendTimestamp{,Zoned}` against
`BenchmarkAppendTimestamp{,Zoned}Stdlib`, so the ratio below can be
re-measured rather than taken on trust — medians of three runs:

| | UTC | offset zone |
|---|---:|---:|
| `time.Time.AppendFormat` | 317.8 ns | 338.4 ns |
| hand-rolled | **52.72 ns** | **67.21 ns** |
| | **6.0×** | **5.0×** |

`BenchmarkAppendTimestampFallback` measures the guard instead of the fast
path, at **354.4 ns** — slightly MORE than the stdlib control, which is the
expected shape and the reason the row is kept: it does the year check and then
delegates. A fallback row that ever came out *faster* than the control would
mean the fallback had stopped being taken, and the equivalence test would be
the only thing left between a caller and a silently truncated date.

The figures this table replaced (54.75 / 62.8 ns, 5.6× / 5.4×) came from a
throwaway harness run before the change landed; they were the right order of
magnitude and are superseded by numbers anyone can regenerate.

Taken, with two guards: a year outside `[0, 9999]` **falls back to
`AppendFormat`** rather than being spelled at the wrong width, and
`TestAppendTimestampMatchesAppendFormat` sweeps four zones — UTC, +02:00,
−05:30 and +00:45 — over **100 000 instants each**, comparing byte for byte.
`TestAppendTimestampEdgeInstants` adds the zero `Time`, both year boundaries,
a leap day, a millisecond needing two pad zeros, and zone offsets carrying
seconds (which the `Z07:00` verb truncates to whole minutes — so does this).

Both encoders share it, because both declare the same layout — asserted by
`TestJSONTimestampLayoutMatchesTextLayout` so the sharing cannot silently
become wrong.

| | before | after | |
|---|---:|---:|---:|
| `Text_Attrs0` | 396.9 ns | **136.3 ns** | **2.91×** |
| `JSON_Attrs0` | 550.7 ns | **275.3 ns** | **2.00×** |
| `Text_TypeTime` | 1 741 ns¹ | **411.0 ns** | 4.24×¹ |
| `JSON_TypeTime` | 1 953 ns¹ | **595.3 ns** | 3.28×¹ |

¹ against the original baseline, so it also carries §5.1's gain.

### Not taken

`internal/service/logger/text_handler.go` — the legacy fused `TextHandler` —
renders its own timestamp through `AppendFormat` with the same layout and would
take the same 5-6×. It is outside this package and its output is pinned by its
own golden tests; left alone deliberately rather than missed.

## 6. What is NOT worth optimising here

- **Allocation.** Every line in this report is `0 B/op, 0 allocs/op`, at every
  attribute count, every type, both encoders, with and without groups and trace
  context. There is nothing to reclaim.
- **`appendSanitizedMessage`** (the V110 framing-byte scrub) was 4.46 % flat
  before the two fixes and is proportionally larger now, but it is a byte loop
  over a short string and there is no cheaper way to do it that does not stop
  doing it. Leave it.
- **`strconv.AppendFloat`.** `KindFloat64` is the most expensive *rendered*
  type (494 ns for four) and all of it is shortest-round-trip conversion. That
  is the correct output; a faster float format would be a different output.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them. `ns/op` is load-sensitive; `allocs/op` is not, and every allocation
> claim above held identically across every run at every load level observed.

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
| Bench wall-clock   | `-benchtime=1s -count=3`; median published, spread below |
| 1-min load average | 0.47 (before-run), 1.32 (after-run) — both well under the 8-core box's noise floor |

Spread across the three counts was under 1.5 % on every line except
`Text_TraceContext` (428.7 / 439.7 / 472.8 ns, 10 %); its median is published
and it is the one line in this report whose ns/op should be read as
approximate.

A later `-count=5` confirmation run at load 1.64 reproduced the three shortest
lines within 2 % — `Text_Attrs0` 136.6, `JSON_Attrs0` 270.0, `Text_Attrs4`
362.7 ns — and an intervening run at load 4.94 put `Text_Attrs0` at 148.8 ns
(+9 %) while the longer lines moved under 1 %. That is the load sensitivity in
one number: **the shorter the benchmark, the more of it is scheduler.** Lines
under ~200 ns in this report should be read as ±10 %; the ratios they support
held at every load observed.

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/logger/encoder
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkText_Attrs0-8          	 8557980	       136.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkJSON_Attrs0-8          	 4078527	       275.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkText_Attrs4-8          	 3199802	       366.8 ns/op	       0 B/op	       0 allocs/op
BenchmarkJSON_Attrs4-8          	 1356955	       882.7 ns/op	       0 B/op	       0 allocs/op
BenchmarkText_Attrs16-8         	 1207426	       989.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkJSON_Attrs16-8         	  451305	      2649 ns/op	       0 B/op	       0 allocs/op
BenchmarkText_TypeInt-8         	 4404676	       270.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkJSON_TypeInt-8         	 2771265	       431.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkText_TypeFloat-8       	 2439093	       494.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkJSON_TypeFloat-8       	 1701818	       706.6 ns/op	       0 B/op	       0 allocs/op
BenchmarkText_TypeTime-8        	 2897398	       411.0 ns/op	       0 B/op	       0 allocs/op
BenchmarkJSON_TypeTime-8        	 2019399	       595.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkText_TypeDuration-8    	 3067384	       393.4 ns/op	       0 B/op	       0 allocs/op
BenchmarkJSON_TypeDuration-8    	 1938636	       620.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkText_TypeError-8       	 5873095	       201.7 ns/op	       0 B/op	       0 allocs/op
BenchmarkJSON_TypeError-8       	 3015068	       399.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkText_TypeGroup-8       	 6049990	       200.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkJSON_TypeGroup-8       	 2996030	       397.9 ns/op	       0 B/op	       0 allocs/op
BenchmarkText_EscapePlain-8     	 3599163	       332.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkJSON_EscapePlain-8     	 1537861	       784.6 ns/op	       0 B/op	       0 allocs/op
BenchmarkText_EscapeQuoted-8    	  855687	      1413 ns/op	       0 B/op	       0 allocs/op
BenchmarkJSON_EscapeQuoted-8    	 1347010	       889.2 ns/op	       0 B/op	       0 allocs/op
BenchmarkText_EscapeControl-8   	  853278	      1404 ns/op	       0 B/op	       0 allocs/op
BenchmarkJSON_EscapeControl-8   	 1345593	       895.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkText_GroupPrefix-8     	 2479098	       483.2 ns/op	       0 B/op	       0 allocs/op
BenchmarkJSON_GroupPrefix-8     	 1000000	      1168 ns/op	       0 B/op	       0 allocs/op
BenchmarkText_TraceContext-8    	 2771361	       439.7 ns/op	       0 B/op	       0 allocs/op
BenchmarkJSON_TraceContext-8    	 1000000	      1025 ns/op	       0 B/op	       0 allocs/op
```
