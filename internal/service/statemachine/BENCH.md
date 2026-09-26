<!-- generated from internal/service/statemachine/agenda_bench_test.go — run `cd internal/service && GOWORK=off go test -run=NONE -bench=. -benchmem -benchtime=1s -count=3 ./statemachine/` to refresh -->
# Benchmarks — `internal/service/statemachine`

One claim, measured: **finding the next transition due costs O(log N), not
O(N)**. The agenda keeps one heap entry per entity — its earliest automatic
transition — so a pass pops what is due and looks at nothing else. The loop it
replaces re-read and decoded every entity of the store on every wake.

## Reproducibility envelope

> **Numbers vary across machines.** The *shape* is what travels: the agenda's
> cost is flat across two orders of magnitude of entities, the sweep's grows
> with them.

| Dimension | Value |
|---|---|
| CPU cores          | 10 (Apple M1 Pro) |
| RAM                | 16 GiB |
| OS / kernel        | macOS 26.6.2 (Darwin 25.6.0) |
| Architecture       | arm64 |
| Go toolchain       | go1.27.1 darwin/arm64 |
| Git branch         | `feat/framework-wave-3b` |
| Git commit         | `c2aea80` + the review fixes (pre-commit) |
| Generated (UTC)    | 2026-09-26 02:55 UTC |
| Bench wall-clock   | `-benchtime=1s -count=3`, 24 s total |

## What is measured

- **`BenchmarkNextDue`** — N entities cycle between two states on a delay of N
  milliseconds, each having entered its state a millisecond after the previous
  one, so exactly one falls due per millisecond and the agenda always holds N
  entries. One iteration: the clock moves a millisecond, `Step` fires the one
  due transition (read, hooks, replace, record, reschedule) and re-evaluates
  the one fired the pass before. Everything a transition costs is in the
  number — the heap is the only part that depends on N.
- **`BenchmarkSweepBaseline`** — the same population, the same millisecond,
  and only the SEARCH of the loop the agenda replaces: range over the store,
  decode every entity, compare its due instant with now. It fires nothing, so
  it is a lower bound of that loop's cost per wake.

## Results

```
BenchmarkNextDue/entities=1000-10         	  335331	      3645 ns/op	    2542 B/op	      29 allocs/op
BenchmarkNextDue/entities=1000-10         	  339094	      3746 ns/op	    2543 B/op	      29 allocs/op
BenchmarkNextDue/entities=1000-10         	  353571	      3725 ns/op	    2543 B/op	      29 allocs/op
BenchmarkNextDue/entities=10000-10        	  336592	      4061 ns/op	    2288 B/op	      29 allocs/op
BenchmarkNextDue/entities=10000-10        	  354188	      3841 ns/op	    2301 B/op	      29 allocs/op
BenchmarkNextDue/entities=10000-10        	  331010	      3821 ns/op	    2283 B/op	      29 allocs/op
BenchmarkNextDue/entities=100000-10       	  289802	      4103 ns/op	    1423 B/op	      30 allocs/op
BenchmarkNextDue/entities=100000-10       	  303241	      4088 ns/op	    1429 B/op	      29 allocs/op
BenchmarkNextDue/entities=100000-10       	  314904	      4028 ns/op	    1428 B/op	      29 allocs/op
BenchmarkSweepBaseline/entities=1000-10   	    2512	    478301 ns/op	  107439 B/op	    2014 allocs/op
BenchmarkSweepBaseline/entities=1000-10   	    2512	    477834 ns/op	  107436 B/op	    2014 allocs/op
BenchmarkSweepBaseline/entities=1000-10   	    2524	    477742 ns/op	  107451 B/op	    2014 allocs/op
BenchmarkSweepBaseline/entities=10000-10  	     229	   5305130 ns/op	 1388870 B/op	   20023 allocs/op
BenchmarkSweepBaseline/entities=10000-10  	     193	   6797628 ns/op	 1389051 B/op	   20023 allocs/op
BenchmarkSweepBaseline/entities=10000-10  	     217	   5450844 ns/op	 1388812 B/op	   20023 allocs/op
BenchmarkSweepBaseline/entities=100000-10 	      16	  70110594 ns/op	16129541 B/op	  200036 allocs/op
BenchmarkSweepBaseline/entities=100000-10 	      16	  68678331 ns/op	16129363 B/op	  200036 allocs/op
BenchmarkSweepBaseline/entities=100000-10 	      16	  67868763 ns/op	16129038 B/op	  200036 allocs/op
```

## Reading

| Entities | Agenda, one due transition fired | Sweep, search only | Ratio |
|---:|---:|---:|---:|
| 1 000   | 3.7 µs | 0.48 ms | ×130 |
| 10 000  | 3.9 µs | 5.3 ms  | ×1 360 |
| 100 000 | 4.1 µs | 68 ms   | ×16 600 |

- **The agenda is flat.** A hundredfold more entities costs about 10 % more
  per transition — the heap's log N, on top of a transition's fixed cost — and
  the allocations do not move: 29 per pass, all of them the transition's own
  (the JSON of the in-memory store, the record's step, the hooks' context)
  and the flights of the two reads, which let a delete that lands during a
  read win over it. The journal's per-key gates allocate nothing: a key's gate
  is one of 256 mutexes, picked by its hash.
- **The sweep is linear**, in time and in memory: it decodes every entity to
  find one — two allocations and about 160 bytes per entity per wake — so at
  100 000 entities every wake costs 68 ms and 16 MB of garbage, whether or not
  anything is due.
- **A write costs the same as a due transition.** A write marks one entity
  dirty and the next pass re-evaluates that entity alone; the sweep re-read
  everything on every write as well, which is why it paced itself at one run a
  second. The agenda keeps the pace (`Config.MinGap`) to coalesce bursts, not
  to survive them.
- **Memory is bounded.** A reschedule leaves the old entry to be dropped when
  it surfaces; the heap is rebuilt once stale entries outnumber live ones two
  to one, so it never holds more than 2N + 64 entries —
  `TestTheHeapIsRebuiltBeforeStaleEntriesOutgrowItsBound`.
