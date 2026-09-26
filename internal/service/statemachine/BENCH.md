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
| Git commit         | `679aad1` (pre-commit) |
| Generated (UTC)    | 2026-09-26 |
| Bench wall-clock   | `-benchtime=1s -count=3`, 25 s total |

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
BenchmarkNextDue/entities=1000-10         	  359516	      3366 ns/op	    2544 B/op	      27 allocs/op
BenchmarkNextDue/entities=1000-10         	  396308	      3281 ns/op	    2546 B/op	      27 allocs/op
BenchmarkNextDue/entities=1000-10         	  411684	      3314 ns/op	    2546 B/op	      27 allocs/op
BenchmarkNextDue/entities=10000-10        	  373569	      3542 ns/op	    2315 B/op	      27 allocs/op
BenchmarkNextDue/entities=10000-10        	  401763	      3638 ns/op	    2332 B/op	      27 allocs/op
BenchmarkNextDue/entities=10000-10        	  388644	      3477 ns/op	    2324 B/op	      27 allocs/op
BenchmarkNextDue/entities=100000-10       	  341588	      3796 ns/op	    1427 B/op	      27 allocs/op
BenchmarkNextDue/entities=100000-10       	  336648	      3852 ns/op	    1427 B/op	      27 allocs/op
BenchmarkNextDue/entities=100000-10       	  307464	      4645 ns/op	    1429 B/op	      27 allocs/op
BenchmarkSweepBaseline/entities=1000-10   	    2217	    508931 ns/op	  107420 B/op	    2014 allocs/op
BenchmarkSweepBaseline/entities=1000-10   	    2486	    530697 ns/op	  107409 B/op	    2014 allocs/op
BenchmarkSweepBaseline/entities=1000-10   	    2574	    474785 ns/op	  107447 B/op	    2014 allocs/op
BenchmarkSweepBaseline/entities=10000-10  	     222	   5346917 ns/op	 1389021 B/op	   20023 allocs/op
BenchmarkSweepBaseline/entities=10000-10  	     231	   5176008 ns/op	 1388937 B/op	   20023 allocs/op
BenchmarkSweepBaseline/entities=10000-10  	     222	   5301756 ns/op	 1389005 B/op	   20023 allocs/op
BenchmarkSweepBaseline/entities=100000-10 	      18	  66491407 ns/op	16129253 B/op	  200036 allocs/op
BenchmarkSweepBaseline/entities=100000-10 	      16	  68055526 ns/op	16129872 B/op	  200035 allocs/op
BenchmarkSweepBaseline/entities=100000-10 	      16	  68656242 ns/op	16128725 B/op	  200035 allocs/op
```

## Reading

| Entities | Agenda, one due transition fired | Sweep, search only | Ratio |
|---:|---:|---:|---:|
| 1 000   | 3.3 µs | 0.50 ms | ×150 |
| 10 000  | 3.6 µs | 5.3 ms  | ×1 470 |
| 100 000 | 3.8 µs | 67 ms   | ×17 600 |

- **The agenda is flat.** A hundredfold more entities costs about 15 % more
  per transition — the heap's log N, on top of a transition's fixed cost — and
  the allocations do not move: 27 per pass, all of them the transition's own
  (the JSON of the in-memory store, the record's step, the hooks' context).
- **The sweep is linear**, in time and in memory: it decodes every entity to
  find one — two allocations and about 160 bytes per entity per wake — so at
  100 000 entities every wake costs 67 ms and 16 MB of garbage, whether or not
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
