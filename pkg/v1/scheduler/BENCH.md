<!-- generated from pkg/v1/scheduler/scheduler_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./v1/scheduler/` to refresh -->
# Benchmarks — `pkg/v1/scheduler`

Five-field POSIX cron and fixed intervals (ADR 0041). A schedule is **parsed
once per process** and **evaluated once per fire, per job**, so the two halves
have different budgets and only one of them is on a path that repeats.

## Every evaluation allocates ZERO, and that is the headline

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `Every(30s)` → `Next` | 10.34 | 0 | **0** |
| `* * * * *` → `Next` | 319.3 | 0 | **0** |
| `0 9 1 * MON` → `Next` (day-field OR) | 1 427 | 0 | **0** |
| `30 3 * * *` → `Next` (daily) | 4 014 | 0 | **0** |
| `30 3 29 2 *` → `Next` (29 February) | 9 530 | 0 | **0** |
| `30 2 * * *` in Europe/Paris, across spring-forward | 11 197 | 0 | **0** |

An engine holding a thousand jobs computes a thousand next-fire times and
allocates nothing doing it. That is the property worth protecting; the
nanoseconds are not.

## The search skips by field, and the numbers prove it

`Next` clearly searches rather than solving in closed form — the cost tracks how
far away the answer is. What matters is **how badly** it scales, and the answer
is: not linearly.

| expression | next fire is roughly | ns/op | vs `* * * * *` |
|---|---|---:|---:|
| `* * * * *` | 1 minute away | 319.3 | 1× |
| `30 3 * * *` | 17 hours ≈ 1 020 minutes | 4 014 | **12.6×** |
| `30 3 29 2 *` | 11 months ≈ 480 000 minutes | 9 530 | **29.8×** |

A minute-by-minute walk would make the 29-February case about **1 500×** the
cheap one. It costs 30×. The search advances by field — skipping whole days,
months and years it can rule out — which is what keeps the worst realistic
expression under 10 µs instead of over a millisecond.

`29 February` is deliberately the sparsest expression the supported subset can
express: it matches roughly once every four years, and it is the shape that
breaks a naive implementation.

## The DST row is the most expensive, and it should be

11 197 ns for `30 2 * * *` in a zone whose 02:30 is about to stop existing.
ADR 0041 decided that a non-existent wall-clock time **does not fire** and a
repeated one **fires once**, and this row is what those two decisions cost:
resolving a wall-clock instant against a zone with a transition in range is
strictly more work than resolving it against UTC.

It is still 11 µs, once per fire. A job firing every minute spends **0.000019 %**
of its interval deciding when to fire next.

## Fixed intervals are a different order of magnitude, on purpose

`Every(30s)` evaluates in **10.34 ns** — an addition — against 319.3 ns for the
cheapest cron expression. The 31× gap is the price of a *calendar*: cron answers
"the next 03:30 in this zone", which has to consider months, days, weekdays and
transitions, while an interval answers "thirty seconds after that one".

The actionable form: **if the schedule does not need to be aligned to a
calendar, `Every` is not just simpler, it is thirty times cheaper.** Neither is
expensive enough to matter for one job; it matters at ten thousand.

## Parsing is the startup path, and refusing is cheaper than accepting

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `Parse("* * * * *")` | 1 275 | 384 | 8 |
| `Parse` of a fully-populated expression | 2 111 | 384 | 8 |
| `Parse("@reboot")` — refused by name | **333.8** | 272 | 2 |
| `Every(30s)` | 20.30 | 16 | 1 |

A dense expression — every field carrying a list, a range and a step — costs
1.66× a trivial one and **the same eight allocations**, so the allocation count
is a property of the parser's shape and not of the input's complexity.

A refusal costs a quarter of an acceptance. That matters more than it looks: an
expression can come from configuration a stranger wrote, and ADR 0041 refuses a
long list of things BY NAME (six- and seven-field forms, `@reboot`, `@every`,
Quartz `L`/`W`/`#`/`?`, 7-for-Sunday, and any expression matching no date). Those
refusals are on the cheap side of the parser, not the expensive one.

## Conclusion: nothing here is worth optimising

Stated with the numbers that support it rather than left implied. Parsing
happens once per process — a service registering two hundred jobs spends
**255 µs** on it, at boot, before serving anything. Evaluation happens once per
fire and allocates nothing, and its worst realistic case is 11 µs against
intervals measured in seconds.

The one thing worth *protecting* is the zero in the allocation column, which is
what a thousand-job engine depends on.

## Reproducibility envelope

> **Numbers vary across machines.** The allocation column is exact and does not
> vary; the nanoseconds do, and the RATIOS — search-vs-distance, cron-vs-
> interval, refusal-vs-acceptance — are what this report asserts. The DST row
> additionally depends on the host's zoneinfo database and self-skips without
> one.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | 47a9e94 |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/pkg/v1/scheduler
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkParse_EveryMinute-8   	  892299	      1275 ns/op	     384 B/op	       8 allocs/op
BenchmarkParse_Dense-8         	  558645	      2111 ns/op	     384 B/op	       8 allocs/op
BenchmarkParse_Refused-8       	 3686379	       333.8 ns/op	     272 B/op	       2 allocs/op
BenchmarkEvery-8               	59569395	        20.30 ns/op	      16 B/op	       1 allocs/op
BenchmarkNext_EveryMinute-8    	 3768010	       319.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkNext_Daily-8          	  291364	      4014 ns/op	       0 B/op	       0 allocs/op
BenchmarkNext_Sparse-8         	  133281	      9530 ns/op	       0 B/op	       0 allocs/op
BenchmarkNext_DayFieldOr-8     	  807582	      1427 ns/op	       0 B/op	       0 allocs/op
BenchmarkNext_AcrossDST-8      	  119160	     11197 ns/op	       0 B/op	       0 allocs/op
BenchmarkNext_Every-8          	100000000	        10.34 ns/op	       0 B/op	       0 allocs/op
ok  	github.com/kitsunium/sdk/pkg/v1/scheduler	11.937s
```
