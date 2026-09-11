<!-- generated from pkg/v1/lock/lock_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./v1/lock/` to refresh -->
# Benchmarks — `pkg/v1/lock`

Named exclusive leases over one process or one machine (ADR 0052). The two
backends differ by three orders of magnitude, and the reason is a design
decision the ADR states rather than an accident — which makes the number a
placement rule.

## A file lease costs 1.6 milliseconds to take and give back

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `Memory` acquire + release | 800.7 | 272 | 3 |
| **`File` acquire + release** | **1 634 844** | 920 | 11 |
| ratio | **2 042×** | | |

ADR 0052 says the file backend `fsync`s its fencing token into the lock file so
that a token survives a reboot — because a restarted fence would reissue numbers
the protected resource has already accepted. That durability is paid **on every
acquisition**, and 1.6 ms is what it costs on this device.

**The rule that follows**: a file lease is for a leader election, a migration, a
cron singleton — something taken seconds or minutes apart. It cannot sit on a
per-request path: at 1.6 ms per acquire/release, one process caps out around
600 critical sections per second even if the protected work is free.

This is the third domain to land on the same number from a different direction.
`vfs` measured a publication at two device round trips, `session`'s file store
measured `Load` at 1.7 ms, `queue` measured durability at 1 865× a round trip.
One device, one answer, and each domain found it independently.

## Refusing a HELD file lock is cheaper than refusing a held memory one

| | ns/op | allocs |
|---|---:|---:|
| `Memory.TryAcquire`, already taken | 109.1 | **0** |
| `File.TryAcquire`, already taken | **34.92** | **0** |

That inversion is worth stating because it is the opposite of what the acquire
row suggests. A non-blocking `flock` on a descriptor somebody else holds fails
in the kernel immediately — no write, no flush, nothing to sync — while the
memory locker still takes its mutex and consults its map.

So the expensive half of the file backend is **succeeding**, not failing. A
process polling a contended file lock pays 35 ns per attempt; the cost arrives
only when it wins.

Both refusals allocate nothing, which is what makes a polling loop viable at
all.

## The cheap operations, and one that must stay cheap

| | ns/op | allocs |
|---|---:|---:|
| `Lease.Fence()` | **2.796** | **0** |
| `Lease.Extend()` (memory) | 154.7 | **0** |
| `Memory.TryAcquire`, free | 785.4 | 3 |

`Fence` at 2.8 ns is a field read, and it is benchmarked to keep it one. A
fencing token is read once per protected operation by anything that compares it
— ADR 0052 is explicit that the SDK can only ISSUE a fence and that mutual
exclusion is not guaranteed where the resource cannot compare it — so a caller
that *does* compare it reads this on its hot path. A `Fence()` that ever became
a computation would tax exactly the callers who took the ADR's advice.

`Extend` at 154.7 ns and zero allocations is what a `Keepalive` pays on its
timer for the whole duration of a protected section. It is cheap enough that the
renewal interval can be chosen for safety rather than for cost.

## Contention is serialisation, and the number says so honestly

`Memory` acquire+release under eight goroutines contending for **one name** is
1 594 ns against 800.7 ns serial — **1.99×**, not 8×.

Read it correctly: this is not scaling, it is the benchmark measuring what a
lock is *for*. Eight goroutines cannot hold one lease at once, so throughput is
capped by the critical section, and 1.99× is the mutex hand-off overhead on top
of a section that does nothing. A real section does work, and then this number
disappears into it.

## A note on running the file rows at all

`BenchmarkFile_*` do not hand `b.TempDir()` to the locker. This container's
`TMPDIR` carries a POSIX ACL that leaves directories at 0775, and the file
locker REFUSES a world-writable non-sticky directory — correctly, since
unlinking the lock file there would hand the next process a different inode,
which is exactly the failure the refusal exists to prevent. The benchmark makes
its own 0700 directory rather than skipping, so the durable path is measured
instead of silently unmeasured.

## Reproducibility envelope

> **Numbers vary across machines** — and the `File` acquire row varies with the
> DEVICE rather than the CPU, since it is `fsync` latency. An NVMe and a network
> volume differ by an order of magnitude. The allocation column is exact.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | 5452223 |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/pkg/v1/lock
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkMemory_AcquireRelease-8           	 1627870	       800.7 ns/op	     272 B/op	       3 allocs/op
BenchmarkFile_AcquireRelease-8             	     746	   1634844 ns/op	     920 B/op	      11 allocs/op
BenchmarkMemory_TryAcquire_Free-8          	 1499287	       785.4 ns/op	     272 B/op	       3 allocs/op
BenchmarkMemory_TryAcquire_Taken-8         	10908162	       109.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkFile_TryAcquire_Taken-8           	34268750	        34.92 ns/op	       0 B/op	       0 allocs/op
BenchmarkMemory_Extend-8                   	 7821909	       154.7 ns/op	       0 B/op	       0 allocs/op
BenchmarkMemory_Fence-8                    	428112454	         2.796 ns/op	       0 B/op	       0 allocs/op
BenchmarkMemory_AcquireReleaseParallel-8   	  826438	      1594 ns/op	     303 B/op	       3 allocs/op
ok  	github.com/kitsunium/sdk/pkg/v1/lock	9.864s
```
