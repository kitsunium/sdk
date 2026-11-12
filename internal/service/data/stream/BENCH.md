<!-- generated from internal/service/data/stream/stream_bench_test.go — run `cd internal && GOWORK=off go test -run=NONE -bench=. -benchmem -benchtime=1s ./service/data/stream/` to refresh -->
# Benchmarks — `internal/service/data/stream`

The memory engine (#819): what one `Append` and one `Read` cost when nothing
is flushed, and what placing a key costs. These are the floor every durable
engine of #890 is read against — the file engine's `Append` (#821) adds one
sync per batch on top of the copy measured here, the SQL engine's (#822) one
statement.

## Reproducibility envelope

> **Numbers vary across machines, and these were taken on a loaded one.** The
> run shared the machine with seven other build lanes (load average 20 to 34
> on 10 cores), single run each, so a figure here can be off by a factor of
> two; `BenchmarkAppendKeyed/keyed` coming out faster than `unkeyed` is that
> noise, not a property of the code. Re-run on a quiet machine before quoting
> a figure. What travels is the allocation count, which noise does not move.

| Dimension | Value |
|---|---|
| CPU cores          | 10 (Apple M1 Pro) |
| RAM                | 16 GiB |
| OS / kernel        | macOS 26.6.2, Darwin 25.6.0 |
| Architecture       | arm64 |
| Go toolchain       | go1.27.3 darwin/arm64, `GOMAXPROCS=10` (the `-10` suffix) |
| Git branch         | `feat/sdk-lot-1-stream` |
| Git commit         | on `91e14de7d` (issue #819) |
| Generated (UTC)    | 2026-11-12 |
| Bench wall-clock   | `-test.benchtime=1s`, single run each; about 20 s in all |

## Results

```
BenchmarkAppend/64B/1-10   	 2479694	       677.4 ns/op	  94.48 MB/s	     685 B/op	       2 allocs/op
BenchmarkAppend/64B/16-10  	  300981	      3670 ns/op	 279.03 MB/s	    8340 B/op	       2 allocs/op
BenchmarkAppend/64B/256-10 	   26180	     79076 ns/op	 207.19 MB/s	  129256 B/op	       2 allocs/op
BenchmarkAppend/4096B/1-10 	  791910	      1397 ns/op	2931.27 MB/s	    4736 B/op	       2 allocs/op
BenchmarkAppend/4096B/16-10         	  117900	     18935 ns/op	3461.10 MB/s	   72458 B/op	       2 allocs/op
BenchmarkAppend/4096B/256-10        	   13567	     88692 ns/op	11822.62 MB/s	 1156301 B/op	       3 allocs/op
BenchmarkRead/1-10                  	10229443	       117.8 ns/op	 543.07 MB/s	     160 B/op	       2 allocs/op
BenchmarkRead/16-10                 	 1212133	       970.5 ns/op	1055.14 MB/s	    2560 B/op	       2 allocs/op
BenchmarkPartitionOf/8B-10          	268891243	         4.410 ns/op	       0 B/op	       0 allocs/op
BenchmarkPartitionOf/32B-10         	63318970	        19.24 ns/op	       0 B/op	       0 allocs/op
BenchmarkPartitionOf/128B-10        	 8994681	       147.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppendKeyed/unkeyed-10     	  436396	      5747 ns/op	    8993 B/op	       2 allocs/op
BenchmarkAppendKeyed/keyed-10       	  317780	      3415 ns/op	    8692 B/op	       2 allocs/op
```

## What the numbers say

- **An Append is two allocations, whatever the batch.** One arena holds every
  byte of the batch, copied so the producer may reuse its buffers at once, and
  the partition's entry slice grows by amortised doubling (the third
  allocation of the 256 × 4 KiB row is that growth landing inside the
  measured window). Nothing is allocated per record.
- **A Read is two allocations, whatever the batch**: the reader's entries and
  one buffer for their bytes, so a reader may keep or scribble on what it was
  given without reaching the engine's copy.
- **Placing a key allocates nothing.** `PartitionOf` is FNV-1a inlined
  (`core/data/stream/partition.go`), one multiply per key byte; on a key of
  the size a business identifier has it is noise beside the copy of the
  payload.
- **The Append benchmarks run with `RetainBytes` at 64 MiB**, so a run of
  millions of iterations measures an append plus the retention it triggers,
  not the heap growing without end.
