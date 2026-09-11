<!-- generated from internal/service/proc/signal/signal_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./proc/signal/` to refresh -->
# Benchmarks — `internal/service/proc/signal`

**"Is `Notify` something I wire once, or something I can call per request?"**

**Wire it once.** Registration costs **≈7 300 ns**; the per-delivery translation
it buys costs **2.98 ns**. That is a ratio of **≈2 460×**, and it is the only
number from this package a caller needs to remember.

Signal *delivery* latency is the kernel's and is not measured here. Signal
*registration* is this package's, is paid on every `Notify`, and is four orders of
magnitude larger than the work it wraps.

## 1. Registration versus what registration buys

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Notify_Stop_One` — subscribe to 1 signal, then tear down | **7 324** | 655 | 10 |
| `Notify_Stop_Four` — subscribe to 4 signals in one call | 13 440 | 744 | 10 |
| `ToSignal` — the per-delivery translation | **2.982** | 0 | 0 |
| `Forward` — the per-delivery send | 116.9 | 0 | 0 |
| `Stop_Idempotent` — every `stop()` after the first | 3.104 | 0 | 0 |

A subscription costs 7.3 µs and buys deliveries at 3 ns of translation plus 117 ns
of channel handoff. A supervisor that subscribes once at startup pays the 7.3 µs
exactly once, in a place where microseconds are not a currency.

**Four signals in one call cost 13.4 µs; four calls would cost 29.3 µs.** The
delta between the two rows is 6 116 ns for 3 extra signals — **≈ 2.0 µs per
additional signal within one registration**, against 7.3 µs for a whole new one.
So `Notify(SIGTERM, SIGINT, SIGHUP, SIGALRM)` is 2.2× cheaper than four separate
`Notify` calls, and it also spawns one goroutine instead of four. Allocation count
is identical (10) either way — the `osSigs` slice is pre-sized, so extra signals
cost time inside `os/signal`, not garbage.

`stop()` is 3.1 ns after the first call: the `sync.Once` fast path. A teardown
path that calls it defensively from several places pays nothing.

### One caveat on how to read the ≈7 300 ns

`stop()` closes `done` and **returns immediately**; the translation goroutine then
runs its own deferred `signal.Stop` and `close(dst)`. So this row is
*registration + a channel close*, and the **deregistration is not inside it** —
it happens on another goroutine just afterwards. The real cost of a
subscribe/unsubscribe pair is therefore somewhat higher than 7.3 µs, and it is not
separable with a benchmark that does not join the goroutine.

`internal/service/proc/reaper`, whose `Stop` *does* block until its loop goroutine
has exited, measures **86 µs** for the same shape of lifecycle
(`reaper/BENCH.md` §2). The gap between 7.3 µs and 86 µs is a fair estimate of
what waiting for the scheduler actually costs — and it is why the advice
("subscribe once") does not change either way.

## 2. `Relay` is one `kill(2)` per signal and 197 ns of loop

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Relay_Batch1` — 1 delivery | 542.3 | 128 | 1 |
| `Relay_Batch64` — 64 deliveries | 22 340 | 640 | 1 |
| `Relay_ReservedTarget` — target 0 or −1, refused before any delivery | 276.5 | 216 | 3 |

Both rows build and fill their source channel *inside* the timed region, so the
difference between them is 63 extra `kill(2)` calls and nothing else:

```
(22 340 − 542.3) / 63 = 346.0 ns per delivered signal
542.3 − 346.0        = 196.3 ns of per-Relay-call overhead
```

**≈ 346 ns per signal** is a bare `kill(2)` on this kernel — it sits exactly
beside `wait4` (283 ns), `setrlimit` (353 ns) and
`exec.Handle_SignalGroup_Live` (390 ns, the same syscall plus the handle's field
reads). The relay adds nothing measurable of its own per signal: the loop body is
a type assertion and a `syscall.Kill`.

The ~196 ns of per-call overhead is the `for range` over the channel plus its
`close` bookkeeping, and it is paid once per `Relay` call, not once per signal. A
relay that runs for the life of a process pays it once.

**The reserved-target refusal costs ≈277 ns and 3 allocations** — the typed
`RelayFailed` with its `target` field. That guard runs on every `Relay` call, but
only *builds* an error when the target is 0 or −1; a legitimate call never reaches
the allocation. (This row has the widest spread in the file, 35 %, because it is a
short allocating path competing with the allocator; min 262 ns, max 358 ns over 8
runs. The median is stable.)

## 3. Nothing here is worth optimising, and here is the arithmetic

Every path in this package is either a syscall or single-digit nanoseconds:

- `ToSignal` is **2.98 ns and zero allocations** — a type assertion and an integer
  re-key. It cannot be made cheaper and it does not need to be.
- `Forward` is **117 ns** for a `select` over a buffered send. It is a channel
  operation; the cost is Go's, and the buffering it exercises is the contract
  (`max(len(sigs), 1)` slots so a burst is not dropped).
- `Relay`'s per-signal cost is **within noise of a raw `kill(2)`**.
- Registration's 7.3 µs is `os/signal` plus a goroutine launch, both outside this
  package.

No production code was changed. The deliverable is the ratio in §1 and the
per-signal decomposition in §2.

## Platform note

This file is untagged and uses only `syscall.SIGTERM` / `SIGINT` / `SIGHUP` /
`SIGALRM` — exactly the constants the package's own untagged internal tests
already use — so it compiles wherever the package does. `Relay` skips rather than
reporting a number where `kill(2)` does not exist: the `!unix` stub returns
`UnsupportedPlatform` before it looks at the channel, and `relaySupported` detects
that by draining an already-closed source. The figures above were taken on Linux;
the Windows relay is a different implementation (`relay_windows.go`) and is not
characterised here.

`SIGALRM` is the subscription signal for the single-signal rows because neither
the benchmark nor the Go runtime raises it, so registering and detaching it
millions of times changes nothing about the process. `Relay` delivers **signal 0**
— the POSIX existence probe — so the kernel performs the full permission walk and
delivers nothing, which is the only way to issue millions of `kill(2)` calls at
the benchmark's own process without perturbing it.

## Reproducibility envelope

> Every figure is the **median of 8 full runs**, each a separate process (8
> rather than 3: at 3 and at 5 the two `Notify` rows and `Relay_ReservedTarget`
> were the noisiest in this domain, so the sample was extended and the last three
> runs were taken at a lower load to check the medians had not drifted — they had
> not, `Notify_Stop_One` moved from 7 314 to 7 324). Load average was 1.06–2.61
> across the runs on this 8-core box.
>
> **Three rows are genuinely wide and stay wide**: `Notify_Stop_One` ±16 %,
> `Notify_Stop_Four` ±21 %, `Relay_ReservedTarget` ±35 %. That is not sampling
> error to be squeezed out — the first two involve the Go runtime's signal M and
> a goroutine launch, and the third is a short allocating path competing with the
> allocator. Their medians are stable across 8 runs; their individual samples are
> not, and a reader should treat them as "≈7 µs" and "≈13 µs", not as four
> significant figures. Every other row is under 8 %.
>
> **The ratios are what this report asserts**: ≈2 460× between registration and
> translation, ≈2.2× between one four-signal `Notify` and four one-signal calls,
> and ≈346 ns per relayed signal. Absolute syscall figures move with kernel
> version and seccomp profile.

| Dimension | Value |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor (8 cores visible) |
| RAM | 15 GiB |
| OS / kernel | Linux 6.12.101+deb13-amd64 |
| Architecture | amd64 |
| Go toolchain | go1.27.1 linux/amd64 |
| Git branch | jaimerias-que-tu-te-connect |
| Git commit | 532a984 |
| Generated (UTC) | 2026-09-10 |
| Bench wall-clock | `-benchtime=1s`, median of 5 runs |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/proc/signal
cpu: AMD EPYC 7351P 16-Core Processor

benchmark                  median ns/op   spread   min–max            B/op  allocs/op
Notify_Stop_One-8                 7 324    16.4%   6 673 – 7 871       655     10
Notify_Stop_Four-8               13 440    21.0%   12 000 – 14 820     744     10
Stop_Idempotent-8                 3.104     1.5%   3.080 – 3.128         0      0
ToSignal-8                        2.982     1.8%   2.970 – 3.024         0      0
Forward-8                         116.9     1.9%   115.9 – 118.1         0      0
Relay_Batch1-8                    542.3     7.5%   532.4 – 573.3       128      1
Relay_Batch64-8                  22 340     6.6%   22 000 – 23 480     640      1
Relay_ReservedTarget-8            276.5    34.5%   262.5 – 358.0       216      3
```
