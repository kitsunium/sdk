<!-- generated from internal/kernel/concur/commit/commit_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=300ms -cpu=1,8 -count=3 ./concur/commit/` to refresh -->
# Benchmarks — `internal/kernel/concur/commit`

Group commit (#803). The question these benchmarks answer is the one the
issue left open: **how much of a group commit's saving does the device already
make?** ext4 and xfs merge concurrent fsyncs in their journal, so N goroutines
each calling `fsync(2)` on one file may already cost about one flush. The pair
`Sync_Direct_Parallel` / `Sync_Commit_Parallel` runs the SAME real fsync on a
file in the benchmark's temporary directory, once per caller and once per
batch.

## Reproducibility envelope

> **Numbers vary across machines — and here across file systems and devices
> more than anywhere.** Read the ratio on your own disk before adopting.

| Dimension | Value |
|---|---|
| CPU cores          | 8 (AMD EPYC 7351P) |
| RAM                | 15 GiB (ballooned VM, shared with other jobs) |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| File system        | the VM's /home volume (`b.TempDir()` under the go test temporary directory) |
| Architecture       | amd64 |
| Go toolchain       | go1.27.3 linux/amd64 |
| Git branch         | `wip/sdk-lot-6-cache` |
| Git commit         | `246113f` (pre-commit) |
| Generated (UTC)    | 2026-11-02 |
| Bench wall-clock   | `-test.benchtime=300ms -cpu=1,8 -count=3` |

## Results

```
BenchmarkSync_Direct_Parallel     	    4780	     76449 ns/op	       0 B/op	       0 allocs/op
BenchmarkSync_Direct_Parallel     	    4899	     73024 ns/op	       0 B/op	       0 allocs/op
BenchmarkSync_Direct_Parallel     	    5036	     73557 ns/op	       0 B/op	       0 allocs/op
BenchmarkSync_Direct_Parallel-8   	   18817	     16986 ns/op	       0 B/op	       0 allocs/op
BenchmarkSync_Direct_Parallel-8   	   22665	     16661 ns/op	       0 B/op	       0 allocs/op
BenchmarkSync_Direct_Parallel-8   	   20761	     18532 ns/op	       0 B/op	       0 allocs/op
BenchmarkSync_Commit_Parallel     	    4498	     68111 ns/op	         1.000 fsyncs/op	     392 B/op	       6 allocs/op
BenchmarkSync_Commit_Parallel     	    5355	     63304 ns/op	         1.000 fsyncs/op	     392 B/op	       6 allocs/op
BenchmarkSync_Commit_Parallel     	    5644	     68137 ns/op	         1.000 fsyncs/op	     392 B/op	       6 allocs/op
BenchmarkSync_Commit_Parallel-8   	   21288	     17104 ns/op	         0.2221 fsyncs/op	     103 B/op	       1 allocs/op
BenchmarkSync_Commit_Parallel-8   	   20130	     21997 ns/op	         0.2309 fsyncs/op	     106 B/op	       1 allocs/op
BenchmarkSync_Commit_Parallel-8   	   19909	     19186 ns/op	         0.2233 fsyncs/op	     102 B/op	       1 allocs/op
BenchmarkDo_Uncontended           	  162826	      1962 ns/op	     392 B/op	       6 allocs/op
BenchmarkDo_Uncontended           	  219915	      1638 ns/op	     392 B/op	       6 allocs/op
BenchmarkDo_Uncontended           	  221710	      1624 ns/op	     392 B/op	       6 allocs/op
BenchmarkDo_Uncontended-8         	  165609	      2001 ns/op	     392 B/op	       6 allocs/op
BenchmarkDo_Uncontended-8         	  176863	      2000 ns/op	     392 B/op	       6 allocs/op
BenchmarkDo_Uncontended-8         	  186500	      2009 ns/op	     392 B/op	       6 allocs/op
```

## What the numbers say

- **The flushes are shared.** At 8 goroutines, `fsyncs/op` is about 0.22: one
  fsync carries four to five callers' items. That is the primitive working —
  the callers that arrive during one flush ride the next.
- **On this device the wall time is not.** `Sync_Commit_Parallel-8` lands at
  ~19 µs per call against ~17 µs for `Sync_Direct_Parallel-8`: the journal
  already merged the concurrent fsyncs, so the group saved syscalls, not
  device time, and paid a goroutine hand-off per batch for it. This is the
  caveat #803 named, measured. The gain a group commit buys on this kind of
  device is for callers that cannot issue their fsyncs concurrently — one
  producer writing a burst, a write path that holds a lock across its flush —
  and on devices or file systems that do not merge.
- **One caller at a time (`-cpu=1`)**, both rows pay one fsync per call
  (~70 µs); the group's hand-off is lost in it.
- **`Do_Uncontended`** (~1.6–2.0 µs, 6 allocations at `-cpu=1`) is the floor: a
  batch, its two channels, the leading goroutine and its context. A `Group`
  around work cheaper than that is a loss; around an fsync (tens of µs to ms)
  it is noise. It is the BATCHED floor: the benchmark's context can end
  (`b.Context()`), so it never takes the idle path of #813 — a caller whose
  context cannot end, on an idle group, flushes in place with no allocation
  for an item of size zero, held by the `allocs: 0` budget `TestPerfAllocsDoIdle`
  (`gen_perf_test.go`, the race-off lane), not timed here.

## What is deliberately not measured

- **An adopter.** `WriteAtomicAll` (vfs), `PublishAll` (queue) and the docstore
  overlay are where the primitive pays — N renames and one directory fsync —
  and they belong to those domains' changes (see `CLAUDE.md` §Adoption).
