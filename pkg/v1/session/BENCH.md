<!-- generated from pkg/v1/session/session_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./v1/session/` to refresh -->
# Benchmarks — `pkg/v1/session`

Server-side sessions (ADR 0045). **`Load` runs on every request that carries a
session cookie**, and `Open` runs beside it, so the two together are what an
authenticated request pays before any of its own work starts. That number was
unpublished until now, and one row of it decides a deployment.

## The number that shapes a deployment: durability costs 2 700× per request

| | memory | file | ratio |
|---|---:|---:|---:|
| `New` | 1 434 ns | 1 896 292 ns | **1 322×** |
| `Load` | 624.0 ns | 1 703 065 ns | **2 729×** |

`Load` on the file store is **1.7 milliseconds**, and `Load` is per-request. A
file-backed session store therefore puts roughly **1.7 ms of `fsync` on every
authenticated request**, which caps a single process at a few hundred such
requests per second no matter how fast the handler is.

That is not a defect and not a surprise once measured: `Load` **slides the idle
window**, which is a WRITE — an idle timeout that is not refreshed on use is not
an idle timeout — and the file store seals, publishes by `rename(2)` and flushes
for every one. The same figure appears in `internal/service/vfs/BENCH.md` (a
publication is two device round trips, ~3 ms) and in
`internal/service/queue/BENCH.md` (durability costs 1 865× on a round trip).
Three domains, one device, one answer.

**The rule that follows**: the file store is for a single-process deployment
that must survive a restart, and for tests. A multi-process deployment that
needs both durability and throughput wants a store backed by something with a
network round trip instead of a disk flush — which is why `Store` is a port with
three methods and no registry.

## The memory store

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `New` | 1 434 | 544 | 4 |
| `Load` | 624.0 | 128 | 2 |
| `Save` | 512.4 | 128 | 2 |
| `Regenerate` | 1 229 | 320 | 6 |
| `Load`, 8 goroutines | 739.4 | 128 | 2 |

`New` is the most expensive of the four because it draws a 256-bit identifier
from the CSPRNG; `Load` and `Save` are a map lookup plus the expiry arithmetic.

`Load` under eight goroutines is **739.4 ns against 624.0 ns serial — only
1.18×**, which is the interesting one. `Load` takes the WRITE lock, because it
slides the idle window, so it could have scaled badly and does not: the critical
section is short enough that eight request goroutines barely contend.

`Regenerate` — the login path, and the ONLY call that binds a subject, which is
why session fixation has no spelling in this API — costs 1 229 ns, less than
`New` plus `Load`. It runs once per login rather than once per request.

## The cookie edge

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `Seal` | 1 629 | 1 776 | 10 |
| `Open` | 1 388 | 1 552 | 8 |
| `Open`, tampered | **1 231** | 1 440 | 5 |

An AEAD seal and open, once each per request. Against the memory store's 624 ns
`Load`, the cookie costs **more than the store does** — worth knowing before
optimising the wrong half.

A tampered cookie is rejected in **1 231 ns, cheaper than opening a valid one**.
That is the right direction: a verifier is exposed to input the caller does not
choose, and a refusal that cost more than an acceptance would be an
amplification an attacker gets for free.

## A note on running these at all

`BenchmarkFile_*` do **not** use `b.TempDir()` directly. This container's
`TMPDIR` carries a POSIX ACL that leaves new directories at `0775`, and the file
store REFUSES a location that is not private — correctly, since a
world-readable directory of sealed sessions is a directory of sealed sessions
anybody can copy. The benchmark makes its own `0700` directory rather than
skipping, so the durable path is measured instead of silently unmeasured.

That refusal firing is itself worth recording: the security check is
load-bearing, and it was found by a benchmark rather than by a test.

## Reproducibility envelope

> **Numbers vary across machines** — and the file rows vary with the DEVICE more
> than with the CPU: they are `fsync` latency, so an NVMe and a network volume
> will differ by an order of magnitude. This run shared the box with four other
> jobs. The allocation column is exact; the ratios are what this report asserts.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | b519c60 |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run, machine under load |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/pkg/v1/session
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkMemory_New-8            	  876338	      1434 ns/op	     544 B/op	       4 allocs/op
BenchmarkMemory_Load-8           	 1918668	       624.0 ns/op	     128 B/op	       2 allocs/op
BenchmarkMemory_Save-8           	 2338737	       512.4 ns/op	     128 B/op	       2 allocs/op
BenchmarkMemory_Regenerate-8     	 1000000	      1229 ns/op	     320 B/op	       6 allocs/op
BenchmarkMemory_LoadParallel-8   	 1651434	       739.4 ns/op	     128 B/op	       2 allocs/op
BenchmarkFile_New-8              	     667	   1896292 ns/op	    4007 B/op	      35 allocs/op
BenchmarkFile_Load-8             	     652	   1703065 ns/op	    5919 B/op	      38 allocs/op
BenchmarkSealer_Seal-8           	  710013	      1629 ns/op	    1776 B/op	      10 allocs/op
BenchmarkSealer_Open-8           	  842689	      1388 ns/op	    1552 B/op	       8 allocs/op
BenchmarkSealer_OpenTampered-8   	 1000000	      1231 ns/op	    1440 B/op	       5 allocs/op
ok  	github.com/kitsunium/sdk/pkg/v1/session	12.819s
```
