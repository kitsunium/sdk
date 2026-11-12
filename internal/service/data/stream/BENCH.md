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
| Git commit         | on `91e14de7d` (issue #819); the file engine's rows on `bb88ed73a` (issue #821) |
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

## The file engine (#821)

`NewFile` over `b.TempDir()` on the internal SSD, the wall clock, records of
64 B. Same machine and the same load as above (load average 20 to 25 on 10
cores, seven other build lanes); `-test.benchtime=1s`, Append single run,
Read three runs (`-count=3`). `ns/record` is the elapsed time divided by the
records appended.

```
BenchmarkAppendFile/1-10                  48   33932010 ns/op   0.00 MB/s   33932010 ns/record   22092 B/op   1 allocs/op
BenchmarkAppendFile/16-10                 38   29950030 ns/op   0.03 MB/s    1871877 ns/record   29591 B/op   3 allocs/op
BenchmarkAppendFile/256-10                38   30047237 ns/op   0.55 MB/s     117372 ns/record   71386 B/op   3 allocs/op
BenchmarkReadFile/tail/backlog=1000-10       426441   2741 ns/op   373.52 MB/s   3712 B/op   3 allocs/op
BenchmarkReadFile/tail/backlog=1000-10       431532   2920 ns/op   350.69 MB/s   3712 B/op   3 allocs/op
BenchmarkReadFile/tail/backlog=1000-10       411595   2640 ns/op   387.81 MB/s   3712 B/op   3 allocs/op
BenchmarkReadFile/tail/backlog=1000000-10    469184   2457 ns/op   416.75 MB/s   3712 B/op   3 allocs/op
BenchmarkReadFile/tail/backlog=1000000-10    398796   2833 ns/op   361.44 MB/s   3712 B/op   3 allocs/op
BenchmarkReadFile/tail/backlog=1000000-10    453739   2718 ns/op   376.74 MB/s   3712 B/op   3 allocs/op
BenchmarkReadFile/spread/backlog=1000000-10   13501  81296 ns/op    12.60 MB/s   3712 B/op   3 allocs/op
BenchmarkReadFile/spread/backlog=1000000-10  334910  25959 ns/op    39.45 MB/s   3712 B/op   3 allocs/op
BenchmarkReadFile/spread/backlog=1000000-10  412430   4562 ns/op   224.45 MB/s   3712 B/op   3 allocs/op
```

(`spread/backlog=1000` ran as well; its three runs, 1761, 1775 and 2965 ns/op,
read the same thousand records as `tail` does.)

- **A batch shares its sync.** One record costs one `F_FULLFSYNC` (the
  darwin `fdatasync`, ~30 ms on this loaded disk); 256 records cost two, so a
  record of the 256 batch costs 117 µs against 33.9 ms alone — 1/289, inside
  the 1/10 #821 asks. The device, not the engine, is the whole figure: the
  three rows take the same 30 ms.
- **The backlog costs a read nothing.** Reading 16 records within the
  newest thousand takes 2.5 to 2.8 µs at a backlog of one thousand and of one
  million: the index is a slice indexed by offset, and the segment read is
  one `pread`. `spread` reads across the whole 104 MB of the million-record
  log: its first run met pages the page cache had not kept on a machine
  short of memory (81 µs, a device read), its third ran warm (4.6 µs). What
  a deep read costs is the page cache's, not the index's.
- **A Read is three allocations**: the payload slice and the run's one
  buffer (seglog's `Range`), and the entries; the entries alias that buffer,
  so nothing is copied twice.
- **Linux is not measured here.** The figures are darwin's `F_FULLFSYNC`; a
  Linux `fdatasync` is usually far cheaper, and the ratio between the rows is
  what carries over.

## The subscriber (#823)

`Subscribe` over the memory engine, one group, one partition, records of
64 B appended before the timer starts; each batch is committed after its
handler (`CommitAfterHandler`). `ns/op` is per record delivered. Same
machine, a load average of 16 to 30 on 10 cores; `-test.benchtime=1s`,
`-count=3`, on `31f091c61` with #823's change applied, 2026-11-12. Run with
`cd internal && GOWORK=off go test -run=NONE -bench='^BenchmarkSubscribe$' -benchmem -count=3 ./service/data/stream/`.

```
BenchmarkSubscribe/batch=1-10         	 1456220	      2681 ns/op	  23.87 MB/s	     432 B/op	       7 allocs/op
BenchmarkSubscribe/batch=1-10         	 1000000	      1803 ns/op	  35.49 MB/s	     432 B/op	       7 allocs/op
BenchmarkSubscribe/batch=1-10         	 1010025	      1685 ns/op	  37.99 MB/s	     433 B/op	       7 allocs/op
BenchmarkSubscribe/batch=16-10        	 4337635	       432.3 ns/op	 148.06 MB/s	     177 B/op	       0 allocs/op
BenchmarkSubscribe/batch=16-10        	 2007829	       676.2 ns/op	  94.65 MB/s	     177 B/op	       0 allocs/op
BenchmarkSubscribe/batch=16-10        	 6861765	       245.3 ns/op	 260.95 MB/s	     177 B/op	       0 allocs/op
BenchmarkSubscribe/batch=256-10       	16743651	       182.2 ns/op	 351.26 MB/s	     161 B/op	       0 allocs/op
BenchmarkSubscribe/batch=256-10       	12409358	       201.3 ns/op	 317.93 MB/s	     161 B/op	       0 allocs/op
BenchmarkSubscribe/batch=256-10       	28200126	       503.1 ns/op	 127.22 MB/s	     161 B/op	       0 allocs/op
```

- **The cost is per batch, not per record.** A turn is a Wake, a Read (its
  two allocations), the handler, and a Commit (the memory engine's cursor
  map) — seven allocations a batch, which the per-record rows of 16 and 256
  round down to zero. Batching takes a record from about 1.7 µs to 0.2 µs.
- **The noise is the machine's.** The spread inside each row (245 to 676 ns,
  182 to 503 ns) is the load of the other build lanes; re-run on a quiet
  machine before quoting a figure. The allocation count is what travels.
