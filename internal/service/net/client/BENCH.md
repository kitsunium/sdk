<!-- generated from internal/service/net/client/client_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=200ms -count=5 ./net/client/` and take medians -->
# Benchmarks — `internal/service/net/client`

`pkg/v1/client/BENCH.md` already prices the end-to-end outbound call against a
loopback origin and attributes **5.53 %** of its allocations to
`guard.RoundTrip`. This report measures what is INSIDE that 5.53 %, and it does
so against a **stub transport** — no sockets, no kernel — because the loopback
number cannot see anything smaller than itself.

Two of the things it found were claims this package had written down and its own
code contradicted, and one is a number a consumer needs before following the
advice `pkg/v1/client/BENCH.md` gives them.

> **Every figure below is the median of five runs.** Spreads were 2–4 % on the
> nanosecond columns; the allocation columns are exact and did not vary.
> Machine load at the start of each sweep is stated in the envelope.

## 1. The default configuration paid for an observer it did not have

`guard.go` said, and had said since the package was written:

> the hook is optional; a nil hook costs one comparison per call.

That was true of `observe` in isolation and false of the request. `RoundTrip`
built a `corenet.CallValue` and a closure that writes into it on **every**
response, whether or not a hook existed. A closure that assigns into a local
forces that local onto the heap, so the two arrived together — and the
documented default, `hook == nil`, which is also what this package's own test
harness passes, paid both.

The allocation profile said so plainly. `alloc_objects`, before:

```
Showing nodes accounting for 648258, 100% of 648343 total
Dropped 35 nodes (cum <= 3241)
Showing top 5 nodes out of 17
      flat  flat%   sum%        cum   cum%
    600778 92.66% 92.66%     600778 92.66%  github.com/kitsunium/sdk/internal/service/net/client.(*guard).RoundTrip
     33280  5.13% 97.80%      33280  5.13%  runtime.mallocgc
     10923  1.68% 99.48%      10923  1.68%  testing.(*B).ResetTimer
      3277  0.51%   100%       3277  0.51%  compress/gzip.NewWriterLevel
```

…and, line by line inside it:

```
    180240     180240     34:	call := corenet.CallValue{
    180234     180234     70:	resp.Body = &cappedBody{
    240304     240304     73:		done: func(read int64, err error) {
```

Two of those three — 70 % of the objects — existed only for the observer. The
third, the `cappedBody`, is the response ceiling and is installed
unconditionally on purpose: there is no unbounded mode, and an over-sized body
FAILS rather than arriving truncated.

Building the record and the closure only when `g.hook != nil` leaves exactly the
ceiling. After:

```
    327699     327699     75:	capped := &cappedBody{inner: resp.Body, limit: g.maxBytes}
```

One site, 100 % of the guard's allocations.

`go build -gcflags=-m` still reports `moved to heap: call` and
`func literal escapes to heap` for `guard.go`, and that is not a contradiction:
escape analysis is a statement about the expressions, which now sit INSIDE
`if g.hook != nil`. They escape when that branch runs. The benchmark and
`TestRoundTripAllocatesOnlyTheCeilingWithoutAHook` are what say how often it
does.

| `guard.RoundTrip` + `Close` | ns/op | B/op | allocs | |
|---|---:|---:|---:|---|
| nil hook — **before** | 361.4 | 184 | 3 | |
| nil hook — **after**  | **195.8** | **64** | **1** | **1.85× faster, −2 allocs** |
| with a hook — before | 366.2 | 184 | 3 | |
| with a hook — after  | 350.1 | 184 | 3 | unchanged, as intended |

A refused request is now free in the guard entirely: with the policy's refusal
built once outside the measurement, `RoundTrip` allocates **0** on the refusal
path. The ~35 allocations `pkg/v1/client/BENCH.md` reports for a denied `Get`
are the typed `errs` refusal and the `Get` path around it — not this function.

The comment has been corrected in the same change, and the claim is now gated by
`TestRoundTripAllocatesOnlyTheCeilingWithoutAHook`, which is mutation-checked in
its own doc comment.

## 2. `EscapedPath` was read twice, and it is not a field read

`RoundTrip` called `req.URL.EscapedPath()` for the observation record and
`requestOf` called it again for the value the policy judges. On a plain path
that is two scans and no allocation. On a path where `u.RawPath` is set — which
is to say, on **exactly the percent-encoded paths the safety checks exist to
catch** — `url.URL` re-validates and unescapes, and that allocates.

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `url.URL.EscapedPath`, plain path | 25.3 | 0 | 0 |
| `url.URL.EscapedPath`, `%2F` path | 320.4 | 32 | 1 |

So the adversarial input paid twice. Reading it once and passing it down:

| `guard.RoundTrip` on a `%2F` path | ns/op | B/op | allocs |
|---|---:|---:|---:|
| **before** | 1 095.0 | 248 | 5 |
| **after**  | **532.7** | **96** | **2** |

2.06× faster and three allocations gone — two from §1, one from here. The
arithmetic closes exactly: 96 = 64 (`cappedBody`) + 32 (one `EscapedPath`).

The risk this creates is that `requestOf` now takes a `string` where a method
call used to be, so a later contributor can hand it `req.URL.Path` — the
DECODED form — and the code still compiles and still reads fine while every
policy starts judging `..` where the wire carries `%2e%2e`.
`Test_guard_handsThePolicyTheEscapedPath` exists for that one mistake and is
mutation-checked against it.

## 3. The path checks allocated most on the paths they exist to refuse — and on ordinary ones

`hasEncodedSeparator` lowercased the whole path; `hasDotSegment` lowercased
every segment. `strings.ToLower` returns its input unchanged and free only when
the string has no uppercase ASCII — and:

- `url.URL.EscapedPath` emits **uppercase** hex. `%2F`, never `%2f`.
- a canonical UUID is uppercase.

So the checks allocated a full copy of the path, plus one per uppercase segment,
on a request carrying nothing adversarial at all. On the CPU profile of the
composed policy over a UUID path, before:

```
Showing nodes accounting for 460ms, 61.33% of 750ms total
Showing top 6 nodes out of 74
      flat  flat%   sum%        cum   cum%
     180ms 24.00% 24.00%      370ms 49.33%  strings.ToLower
     130ms 17.33% 41.33%      220ms 29.33%  regexp.(*Regexp).doOnePass
      50ms  6.67% 48.00%       60ms  8.00%  strings.(*Builder).WriteByte (inline)
      40ms  5.33% 53.33%       50ms  6.67%  runtime.mallocgcSmallNoScanSC5
      30ms  4.00% 57.33%       30ms  4.00%  indexbytebody
      30ms  4.00% 61.33%       40ms  5.33%  runtime.mallocgcSmallNoScanSC6
```

**49.33 % of the CPU, and 97.93 % of the allocated objects, was lowercasing the
path so it could be compared against six ASCII constants.** The allocation
profile split it 51.41 % `hasEncodedSeparator` / 46.52 % `hasDotSegment`.

Both now fold ASCII in place. After, the same benchmark:

```
     120ms 22.64% 22.64%      340ms 64.15%  regexp.(*Regexp).doOnePass
      60ms 11.32% 33.96%       60ms 11.32%  regexp/syntax.(*Inst).MatchRunePos
      50ms  9.43% 43.40%       80ms 15.09%  regexp.onePassNext
      40ms  7.55% 50.94%       40ms  7.55%  regexp.(*inputString).step
      30ms  5.66% 56.60%       30ms  5.66%  github.com/kitsunium/sdk/internal/service/net/client.foldsASCII
      30ms  5.66% 62.26%       30ms  5.66%  github.com/kitsunium/sdk/internal/service/net/client.hasEncodedSeparator
```

`strings.ToLower` is gone from the profile; the two scans together are 11.3 % of
what the pattern matching now dominates.

| | ns/op before | ns/op after | B/op before → after | allocs before → after |
|---|---:|---:|---|---|
| `checkPath`, plain path | 150.0 | **88.5** | 0 → 0 | 0 → 0 |
| `checkPath`, UUID path | 624.6 | **125.7** | 112 → **0** | 2 → **0** |
| `hasEncodedSeparator`, UUID path | 275.3 | **45.3** | 64 → **0** | 1 → **0** |
| `hasDotSegment`, UUID path | 286.1 | **75.2** | 48 → **0** | 1 → **0** |
| `hasDotSegment`, 9 uppercase segments | 1 016.0 | **172.1** | 128 → **0** | 8 → **0** |
| `Policies(AllowMethods, DenyPaths, AllowPaths)`, plain | 488.6 | **450.9** | 0 → 0 | 0 → 0 |
| `Policies(...)`, UUID path | 2 347.0 | **1 361.0** | 224 → **0** | 4 → **0** |

The composed row is the one that matters, because it is the shape a consumer is
told to write. `checkPath` runs once per PATH policy, so a `DenyPaths` +
`AllowPaths` pair ran it twice — 224 B and 4 allocations, exactly 2× the
isolated figure. **1.72× faster and allocation-free.**

### Why this is safe, and how that is established

A path check is a security boundary, so equivalence with the spelling it
replaced is not argued in prose. It rests on one lemma and is demonstrated three
ways, in `path_equivalence_internal_test.go`:

- **The lemma**: no code point outside ASCII lowercases into any byte the two
  scans compare against (`.`, `%`, `2`, `e`, `f`, `c`, `5`). This is swept over
  the whole Unicode code space, not sampled — a single counter-example would be
  a traversal bypass, and there is no reason to guess which plane it lives in.
  (U+0130 `İ` does fold to a bare ASCII `i`, which is why the sweep exists; `i`
  is not a compared byte.)
- **The corpus**: both implementations, old and new, run over every adversarial
  path a reviewer would think of — mixed-case percent encoding, `%2F`, `%2e%2e`,
  `.%2e`, `%2e.`, doubly-encoded `%252e`, truncated escapes (`/a%2`, `/a%`),
  over-long segments (4 096 chars, 512 repeats), empty segments, `//`, `///`,
  trailing slashes, relative paths, invalid UTF-8 (`\xff`), multi-byte runes,
  and paths with no dot segment at all which must be ADMITTED.
- **The random paths**: 200 000 paths assembled from a fixed seed out of an
  alphabet concentrated on the bytes the scans care about.

## 4. A pattern list is linear, and it is the ADMISSIONS that scale

This is the number a consumer needs and did not have.
`pkg/v1/client/BENCH.md` measures one allow pattern and one deny pattern, then
recommends denying by default and enumerating what you allow. A real service
enumerates 30–50 endpoints. `pathPolicy.Allow` and `denyPolicy.Allow` each run
`regexp.MatchString` per pattern, in order, until one answers.

Patterns of the shape `/v1/resourceN/[^/]+`. Medians of five, allocation-free
everywhere except the refusal, which is the typed `errs` value.

| patterns | matches FIRST | matches LAST | no match |
|---:|---:|---:|---:|
| 1  | 286.1 ns | 288.0 ns | 310.5 ns |
| 5  | 286.8 ns | 553.8 ns | 336.6 ns |
| 10 | 289.4 ns | 871.5 ns | 381.1 ns |
| 25 | 290.0 ns | 1 827.0 ns | 440.5 ns |
| 50 | 286.6 ns | **3 436.0 ns** | 556.7 ns |

Three things fall out, and only the first is the obvious one.

**It is linear, at two very different slopes.** Per pattern tried and rejected:
**64.2 ns** when the pattern shares a prefix with the request path (the "matches
LAST" column — every earlier `/v1/resourceN/…` pattern gets deep into the path
before failing), and **about 5 ns** when it does not (5.0 at fifty patterns,
5.4 at twenty-five — the "no match" column, where `/v1/absent/abc` diverges at
the fifth character). Roughly a **12×** spread, decided
entirely by how much of each pattern the engine has to walk before it can say
no. Pattern *count* is not the variable; **prefix similarity × count** is.

**The denial is the cheap case.** 556.7 ns at fifty patterns. That is the path a
"deny by default" posture runs most often, and it is the one that stays flat.

**The expensive case is admitting the LAST-listed endpoint**, at 3 436 ns.
Against `pkg/v1/client/BENCH.md`'s 186 823 ns loopback `Get` that is **1.8 %**,
and against a real dependency across a network it is a fraction of a percent.

**So the recommendation survives, and this report says so.** Deny by default,
enumerate what you allow. Two qualifications go with it:

- **Order the allow list hot-first.** An allow list short-circuits on a match,
  so the cost of an admitted request is its position, not the length of the
  list. Moving the busiest endpoint from position 50 to position 1 is worth
  3.1 µs per request and costs nothing.
- **A deny list is paid in FULL on every admitted request.** `denyPolicy` can
  only return nil after trying every pattern — a non-match has to be proved
  against all of them. So a long deny list has no best case, while a long allow
  list does. Prefer a precise allow list to a broad allow list plus a long deny
  list, when the two express the same policy.

At 500 patterns the "matches LAST" case extrapolates to ~32 µs, which is where
this stops being noise. Nothing in this package refuses a list that long, and
this paragraph is the only warning there is.

## 5. `Content-Length` as a hint, and the bound that keeps it honest

`Do` read the body with `io.ReadAll`, which starts at 512 bytes and grows by
append, so a large body is reallocated a dozen times and roughly twice its own
size is allocated and copied. `raw.ContentLength` was available and ignored.

The obvious fix is a vulnerability, and it is worth stating plainly because it
is more attractive than the bug it repairs. Sizing the buffer from the peer's
own header up to `MaxResponseSize` lets a peer answering **ten bytes** with
`Content-Length: 8388608` make this client reserve **8 MiB per request** — for
free, and multiplied by every call in flight. That is a memory-amplification
introduced by a performance fix.

So the hint is bounded by `maxPresizedRead` (64 KiB), ignored when negative
(which is what `net/http` reports for a chunked or HTTP/2 body of unknown
length), and — this is the part that is not obvious — **ignored entirely when it
exceeds the bound**, falling back to `io.ReadAll` rather than reserving the
bound. Reserving 64 KiB on the word of a peer claiming 8 MiB would still hand a
liar 64 KiB for nothing.

| body | announced | ns/op | B/op | allocs |
|---|---|---:|---:|---:|
| 1 KiB | honest | **1 318** | **1 832** | **6** |
| 1 KiB | unknown (`-1`) | 1 724 | 2 472 | 9 |
| 64 KiB | honest | **20 202** | **74 025** | **6** |
| 64 KiB | unknown (`-1`) | 50 653 | 138 409 | 21 |
| 1 MiB | honest | 996 472 | 2 228 277 | 29 |
| 1 MiB | unknown (`-1`) | 1 300 239 | 2 228 284 | 29 |

**The two 1 MiB rows are identical on purpose and that is the point of the
row**: 1 MiB is past the bound, so the announced case takes the same
`io.ReadAll` branch as the unknown one. It is not two arms accidentally running
the same code — the 64 KiB pair, where the branch differs, differs by 1.87× in
bytes and 2.51× in time, and the lying-peer rows below prove the fallback fires.

The mid-size row is where this lives: **−47 % allocated bytes, 15 fewer
allocations, 2.51× faster** on a 64 KiB body. That is the size of a real API
response.

| a peer answering **10 bytes** and claiming… | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `Content-Length: 8388608` (the ceiling) | 999.7 | **808** | 6 |
| `Content-Length: 65536` (the bound) | 18 186.0 | 74 025 | 6 |

The first row is the whole design: a colossal claim is past the bound, so
nothing at all is reserved on it. The second is the worst case a lying peer can
reach, and it is bounded at the price of one honest 64 KiB body however large
`MaxResponseSize` is configured. `Test_readBody` asserts that bound on
`cap()` directly — a benchmark is not run in CI — and is mutation-checked
against removing it.

The ceiling itself is untouched: `cappedBody` still refuses an over-sized body
rather than truncating it, whatever buffer it is handed.

## 6. A lowercase header name cost two allocations per request, forever

`http.Header.Get` and `http.Header.Set` both canonicalise their argument, and
that conversion allocates for a name that is neither already canonical nor one
of `net/http`'s interned common names. `applyHeaders` called both, per header,
per request.

Nothing documented the requirement, so `"x-request-source"` — which is how a
caller writes it — cost two allocations per request forever. `"accept"` cost
none, because `Accept` is interned; the tax falls exactly on the CUSTOM headers
people spell in lowercase.

| two default headers, one custom | ns/op | B/op | allocs |
|---|---:|---:|---:|
| configured canonically | 595.4 | 432 | 4 |
| configured lowercase — **before** | 932.6 | 464 | 6 |
| configured lowercase, canonicalised in `New` — **after** | **599.5** | **432** | **4** |

Canonicalising once at construction makes the mis-spelled configuration cost
exactly the same as the correct one. The residual 4 allocations are
`http.Header`'s own shape — one `[]string` per header value — and are not
avoidable. 1.56× on the affected configuration.

## What was refused, and what it would have weakened

Security before speed. Each of these would have been faster and is not here.

- **Memoising `checkPath` across policies.** The composed shape runs it twice
  per request and a cache field on `RequestValue` would halve that.
  `RequestValue` is passed **by value** precisely so a policy cannot mutate the
  request it is authorising — a documented property with its own test. A cache
  field undoes it to save an allocation that no longer exists anyway, now that
  the scans are free.
- **Hoisting `checkPath` out of the policies into `guard.RoundTrip`.** It would
  halve the work and look safer. It is a BEHAVIOUR change: today a caller
  composing only `AllowMethods(...)` gets no path check, and hoisting starts
  refusing dot segments for policy sets that previously passed. That is an ADR,
  not an optimisation.
- **Skipping the `errs` fields on the refusal path.** A refusal is ~2
  allocations here and ~35 through `Get`, and they are the typed refusal and its
  fields — SDK rule 4's Public/Private split, which is what lets a caller route
  on `errs.HasCode` and what keeps the refused path out of the public message.
- **Skipping `cappedBody` when `maxBytes` is large.** There is no unbounded mode
  and there must not be one. The one remaining allocation in `guard.RoundTrip`
  is this wrapper, and it stays.
- **Relaxing `resolve`'s origin refusal.** It is the fix for a real SSRF-shaped
  escape — `Get("//peer/v1/x")` reaching `peer` past an `AllowPaths("/v1/x")`.
  Not touched.
- **Combining the allow patterns into one alternation.** It would turn §4's
  linear scan into a single match. It changes what a malformed pattern does (one
  bad pattern would poison the whole list rather than being refused by name at
  construction), and `regexp`'s leftmost-first semantics across an alternation
  are not the same question as "did any of these anchored patterns match". Not
  worth a semantic change for 3.3 µs against a network call.

## Measurements thrown away, and why

The discipline that produced this report says a table contradicting itself
arithmetically is noise. Three sets were discarded.

1. **The first `readBody`.** It sized a `bytes.Buffer` to `min(hint, 64 KiB)`
   and clamped rather than falling back. It measured **1.85× MORE allocated
   bytes and 2.3× slower** at 1 MiB (4 129 081 B / 2 986 507 ns against
   2 228 280 B / 1 289 074 ns) — worse than the `io.ReadAll` it replaced. Two
   causes, both real: `bytes.Buffer.ReadFrom` reserves `MinRead` before EVERY
   read, so a buffer sized to exactly the body is reallocated once more at the
   end and the hint buys nothing; and clamping a large hint to the bound starts
   the doubling from 64 KiB instead of declining to reserve at all. Fixed by
   adding `MinRead` headroom and falling back above the bound. The result in §5
   is the second implementation; the first is recorded here because it looked
   obviously correct.
2. **The first attempt at mutation 7.** A `sed` on `if g.hook != nil` matched
   BOTH the branch in `RoundTrip` and the guard inside `observe`, so the run
   produced a nil-pointer panic instead of the allocation delta the test claims
   to catch. A panic is not the failure the doc comment should record. Redone
   against `RoundTrip`'s branch alone.
3. **The first attempt at mutation 8.** Hoisting a `CallValue` to the top of
   `RoundTrip` and taking its address did **not** reproduce the allocation —
   escape analysis kept it on the stack, because what forced the original onto
   the heap was a CLOSURE assigning into it, not the hoist. The test passed
   under a mutation that did not model the defect, which is exactly the trap of
   confirming a guard by inspection. Redone by restoring the original function
   body verbatim, which fails at `a refused round trip allocated 1 times,
   want 0`.

One row moved without a code change and is disclosed rather than smoothed:
`url.URL.EscapedPath` on a plain path went from **18.6 ns to 25.3 ns** between
the two sweeps. That is stdlib code this change does not touch, and both
readings were tight across their five runs (18.4–18.8 and 25.0–25.9). It is a
code-layout effect from the surrounding package changing. It is a 7 ns absolute
move on the smallest figure in the report and it does not affect any conclusion
here — but a 40 % shift in untouched code is the kind of thing that should be
written down rather than explained away.

## What these numbers are NOT

They are a stub transport, chosen so the package's own contribution is visible
at all. Against a real dependency every figure here is dwarfed by the network,
which is precisely `pkg/v1/client/BENCH.md`'s point and is not contradicted by
anything above. Nothing in this report argues the client is slow. It argues that
three of its documented properties were not true, that they now are, and that
they are gated.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor |
| CPU cores | 8 |
| RAM | 15 GiB |
| OS / kernel | Linux 6.12.101+deb13-amd64 |
| Architecture | amd64 |
| Go toolchain | go1.27.1 linux/amd64 |
| Git branch | jaimerias-que-tu-te-connect |
| Git commit | c2cb1d3 |
| Generated (UTC) | 2026-09-10 |
| Bench wall-clock | `-test.benchtime=200ms -count=5`, medians |
| Load average (baseline sweep, at start) | 0.54 |
| Load average (final sweep, at start) | 0.54 |

## Results (medians of five)

```
BenchmarkGuardRoundTrip/NilHook-8                               195.8 ns/op        64 B/op     1 allocs/op
BenchmarkGuardRoundTrip/WithHook-8                              350.1 ns/op       184 B/op     3 allocs/op
BenchmarkGuardRoundTripEncoded-8                                532.7 ns/op        96 B/op     2 allocs/op
BenchmarkCheckPath/Plain-8                                       88.5 ns/op         0 B/op     0 allocs/op
BenchmarkCheckPath/UUID-8                                       125.7 ns/op         0 B/op     0 allocs/op
BenchmarkCheckPath/EncodedSeparator-8                           324.9 ns/op       208 B/op     2 allocs/op
BenchmarkCheckPath/EncodedDot-8                                 277.7 ns/op       208 B/op     2 allocs/op
BenchmarkPathScan/Plain/Separator-8                              17.8 ns/op         0 B/op     0 allocs/op
BenchmarkPathScan/Plain/DotSegment-8                             73.4 ns/op         0 B/op     0 allocs/op
BenchmarkPathScan/UUID/Separator-8                               45.3 ns/op         0 B/op     0 allocs/op
BenchmarkPathScan/UUID/DotSegment-8                              75.2 ns/op         0 B/op     0 allocs/op
BenchmarkAllowPathsScaling/1/First-8                            286.1 ns/op         0 B/op     0 allocs/op
BenchmarkAllowPathsScaling/1/Last-8                             288.0 ns/op         0 B/op     0 allocs/op
BenchmarkAllowPathsScaling/1/NoMatch-8                          310.5 ns/op       208 B/op     2 allocs/op
BenchmarkAllowPathsScaling/5/First-8                            286.8 ns/op         0 B/op     0 allocs/op
BenchmarkAllowPathsScaling/5/Last-8                             553.8 ns/op         0 B/op     0 allocs/op
BenchmarkAllowPathsScaling/5/NoMatch-8                          336.6 ns/op       208 B/op     2 allocs/op
BenchmarkAllowPathsScaling/10/First-8                           289.4 ns/op         0 B/op     0 allocs/op
BenchmarkAllowPathsScaling/10/Last-8                            871.5 ns/op         0 B/op     0 allocs/op
BenchmarkAllowPathsScaling/10/NoMatch-8                         381.1 ns/op       208 B/op     2 allocs/op
BenchmarkAllowPathsScaling/25/First-8                           290.0 ns/op         0 B/op     0 allocs/op
BenchmarkAllowPathsScaling/25/Last-8                           1827.0 ns/op         0 B/op     0 allocs/op
BenchmarkAllowPathsScaling/25/NoMatch-8                         440.5 ns/op       208 B/op     2 allocs/op
BenchmarkAllowPathsScaling/50/First-8                           286.6 ns/op         0 B/op     0 allocs/op
BenchmarkAllowPathsScaling/50/Last-8                           3436.0 ns/op         0 B/op     0 allocs/op
BenchmarkAllowPathsScaling/50/NoMatch-8                         556.7 ns/op       208 B/op     2 allocs/op
BenchmarkPoliciesShape/Plain-8                                  450.9 ns/op         0 B/op     0 allocs/op
BenchmarkPoliciesShape/UUID-8                                  1361.0 ns/op         0 B/op     0 allocs/op
BenchmarkDoBodySize/1024/Announced-8                           1318.0 ns/op      1832 B/op     6 allocs/op
BenchmarkDoBodySize/1024/Unknown-8                             1724.0 ns/op      2472 B/op     9 allocs/op
BenchmarkDoBodySize/65536/Announced-8                         20202.0 ns/op     74025 B/op     6 allocs/op
BenchmarkDoBodySize/65536/Unknown-8                           50653.0 ns/op    138409 B/op    21 allocs/op
BenchmarkDoBodySize/1048576/Announced-8                      996472.0 ns/op   2228277 B/op    29 allocs/op
BenchmarkDoBodySize/1048576/Unknown-8                       1300239.0 ns/op   2228284 B/op    29 allocs/op
BenchmarkDoLyingPeer/AtTheCeiling-8                             999.7 ns/op       808 B/op     6 allocs/op
BenchmarkDoLyingPeer/AtTheBound-8                             18186.0 ns/op     74025 B/op     6 allocs/op
BenchmarkApplyHeaders/Canonical-8                               595.4 ns/op       432 B/op     4 allocs/op
BenchmarkApplyHeaders/NonCanonical-8                            932.6 ns/op       464 B/op     6 allocs/op
BenchmarkApplyHeaders/NonCanonicalFixedAtConstruction-8         599.5 ns/op       432 B/op     4 allocs/op
BenchmarkResolve-8                                              884.7 ns/op       384 B/op     4 allocs/op
BenchmarkEscapedPath/Plain-8                                     25.3 ns/op         0 B/op     0 allocs/op
BenchmarkEscapedPath/Encoded-8                                  320.4 ns/op        32 B/op     1 allocs/op
BenchmarkDotSegmentDepth-8                                      172.1 ns/op         0 B/op     0 allocs/op
```

## Baseline (medians of five, at commit c2cb1d3 before this change)

Kept so the deltas above can be re-derived rather than trusted.

```
BenchmarkGuardRoundTrip/NilHook-8                          361.4 ns/op       184 B/op    3 allocs/op
BenchmarkGuardRoundTrip/WithHook-8                         366.2 ns/op       184 B/op    3 allocs/op
BenchmarkGuardRoundTripEncoded-8                          1095.0 ns/op       248 B/op    5 allocs/op
BenchmarkCheckPath/Plain-8                                 150.0 ns/op         0 B/op    0 allocs/op
BenchmarkCheckPath/UUID-8                                  624.6 ns/op       112 B/op    2 allocs/op
BenchmarkCheckPath/EncodedSeparator-8                      623.5 ns/op       256 B/op    4 allocs/op
BenchmarkCheckPath/EncodedDot-8                            360.0 ns/op       216 B/op    3 allocs/op
BenchmarkPathScan/Plain/Separator-8                         47.6 ns/op         0 B/op    0 allocs/op
BenchmarkPathScan/Plain/DotSegment-8                        87.4 ns/op         0 B/op    0 allocs/op
BenchmarkPathScan/UUID/Separator-8                         275.3 ns/op        64 B/op    1 allocs/op
BenchmarkPathScan/UUID/DotSegment-8                        286.1 ns/op        48 B/op    1 allocs/op
BenchmarkAllowPathsScaling/1/Last-8                        309.5 ns/op         0 B/op    0 allocs/op
BenchmarkAllowPathsScaling/5/Last-8                        553.9 ns/op         0 B/op    0 allocs/op
BenchmarkAllowPathsScaling/10/Last-8                       849.5 ns/op         0 B/op    0 allocs/op
BenchmarkAllowPathsScaling/25/Last-8                      1736.0 ns/op         0 B/op    0 allocs/op
BenchmarkAllowPathsScaling/50/Last-8                      3214.0 ns/op         0 B/op    0 allocs/op
BenchmarkPoliciesShape/Plain-8                             488.6 ns/op         0 B/op    0 allocs/op
BenchmarkPoliciesShape/UUID-8                             2347.0 ns/op       224 B/op    4 allocs/op
BenchmarkDoBodySize/1024-8                                2024.0 ns/op      2592 B/op   11 allocs/op
BenchmarkDoBodySize/65536-8                              54013.0 ns/op    138529 B/op   23 allocs/op
BenchmarkDoBodySize/1048576-8                          1417708.0 ns/op   2228396 B/op   31 allocs/op
BenchmarkApplyHeaders/Canonical-8                          599.2 ns/op       432 B/op    4 allocs/op
BenchmarkApplyHeaders/NonCanonical-8                       935.4 ns/op       464 B/op    6 allocs/op
BenchmarkResolve-8                                         871.5 ns/op       384 B/op    4 allocs/op
BenchmarkEscapedPath/Plain-8                                18.6 ns/op         0 B/op    0 allocs/op
BenchmarkEscapedPath/Encoded-8                             380.6 ns/op        32 B/op    1 allocs/op
BenchmarkDotSegmentDepth-8                                1016.0 ns/op       128 B/op    8 allocs/op
```

The baseline's `DoBodySize` rows carry no `Announced`/`Unknown` split because
the read did not consult `Content-Length` at all; they are the `Unknown` arm's
ancestors, and the small deltas against today's `Unknown` rows (2 228 396 →
2 228 280 B, 31 → 29 allocs) are consistent with the two allocations §1 removed
from the guard above them — 116 B against the 120 B the guard shed, the four
bytes being B/op truncation over the iteration count.
