<!-- generated from internal/service/proc/reaper/reaper_unix_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./proc/reaper/` to refresh -->
# Benchmarks — `internal/service/proc/reaper`

**"Can I poll `ReapOnce`, and what does a reaper cost to own?"**

**Yes — 283 ns, zero allocations, no matter how often.** And **no**, do not
construct one per unit of work: a `Start`/`Stop` cycle is **86 µs**, which is 300×
the sweep it wraps, and none of that 86 µs belongs to this package.

## 1. `ReapOnce` on an idle supervisor is one `wait4` and nothing else

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `ReapOnce_NoChildren` — `wait4(-1, WNOHANG)` → `ECHILD` | **283.5** | **0** | **0** |
| `ReapOnce_WithObserver` — same, plus the `WithOnReap` callback | 288.5 | 0 | 0 |

This is the number a supervisor needs before putting `ReapOnce` on a timer, and
it is unambiguous: an idle sweep is a single syscall with no Go-side allocation
at all. At a 1 ms poll interval it costs 0.03 % of a core. At 100 Hz it is
invisible. **There is no polling frequency at which this package's own code
becomes the cost** — the kernel's `wait4` is the whole of it.

The observer costs **5 ns**, so `WithOnReap` can be wired unconditionally for
observability. (It fires once per sweep, not once per child, which is why the
delta is a function-call and not a loop.)

For scale on this box: `wait4` 283 ns, `kill` ~350 ns, `setrlimit` ~353 ns,
`prctl` 274 ns, `getpid` 119 ns. `ReapOnce` sits exactly where a bare syscall
sits, because that is what it is.

## 2. A `Start`/`Stop` cycle is 86 µs, and it is the Go scheduler, not this code

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `StartStop_Cycle` | **86 000** | 441 | 7 |
| `New` | 88.81 | 104 | 2 |
| `New_WithOption` | 93.45 | 104 | 2 |

86 µs is 300× a `ReapOnce` and 1 000× a `New`. Where does it go? The CPU profile
names one thing:

```
      0     0%     0%   3.07s 73.44%  runtime.mcall
  0.06s  1.44%  1.44%   2.99s 71.53%  runtime.schedule
  2.37s 56.70% 58.37%   2.37s 56.70%  runtime.futex          ← goroutine park/wake
  0.04s  0.96%          1.49s 35.65%  runtime.notewakeup
      0     0%           0.38s  9.09%  runtime.ensureSigM.func1  ← os/signal's dedicated M
      0     0%           0.52s 12.44%  reaper.(*unixReaper).loop
```

**56.7 % is `runtime.futex` under the scheduler** — parking and waking an M — and
9 % is `runtime.ensureSigM`, the goroutine the Go runtime spins up the first time
anything calls `signal.Notify`. `reaper.loop` accounts for 12 %, and most of that
is the loop parking on its own select.

This is the price of the contract, not an inefficiency in it. `Stop` blocks on
`<-stopped` **by design**: the package promises that when `Stop` returns, the
loop goroutine is gone, the SIGCHLD subscription is detached, and a final drain
has run — no zombie and no goroutine outlives it. Honouring that requires a full
scheduler round trip. Making it cheaper would mean not waiting, which would mean
not keeping the promise.

**Verdict: nothing to optimise.** The actionable statement is about *usage*: a
reaper is a per-process object. Create it once at startup, `Start` it once, `Stop`
it at shutdown. A test suite or a request handler that constructs one per case
pays 86 µs for the lifecycle and 283 ns for the work.

`New` itself is 89 ns and two small allocations, and creates no channels — those
are made in `Start`. So constructing a reaper you never start is genuinely cheap;
it is the `Start` that is not.

## 3. The two probes

| workload | ns/op | allocs/op |
|---|---:|---:|
| `IsPID1` — `getpid(2)` | 119.0 | 0 |
| `SetChildSubreaper` — `prctl(PR_SET_CHILD_SUBREAPER, 1)` | 273.7 | 0 |

`IsPID1` **is a syscall**, not a constant. 119 ns is cheap, but it is not free and
it will never change its answer for the life of the process — so hoist it out of
any loop that tests it. The package does not cache it, deliberately: a cached pid
is a lie after a `fork` in the wrong direction, and the SDK does not decide for
the caller when to cache.

`SetChildSubreaper` is armed once at startup and is idempotent in the kernel — the
benchmark re-arms an already-armed process, which is why it can be measured
millions of times. 274 ns puts it in the same band as every other syscall here.
Where `prctl` does not exist (every Unix that is not Linux) the row skips rather
than reporting the cost of an `UnsupportedPlatform` return.

## What was NOT measured, and why

**Reaping actual children.** A drain that collects *N* children is *N* × `wait4`
plus the same `ECHILD` terminator, and the per-child cost is the kernel's task
teardown, not this package's loop. Measuring it would need the benchmark to fork
*N* children per iteration, at ~626 µs each (see
`internal/service/proc/exec/BENCH.md` §1) — the spawn would be 99.9 % of every
sample and the reaper would be noise. `ReapOnce_NoChildren` isolates the loop's
own cost, which is the part this package controls.

**SIGCHLD delivery latency.** That is the kernel's, and it is not a property of
this code. What *is* this package's is the registration that makes delivery
possible, and that is inside the 86 µs of §2.

## Reproducibility envelope

> Every figure is the **median of 3 full runs**, each a separate process. Spread
> was under 5.3 % on every row. Load average was 1.64–1.77 during these runs on
> this 8-core box.
>
> **The ratios are what this report asserts**: `ReapOnce` is one syscall,
> `StartStop_Cycle` is 300× it, and the 300× is scheduler cost. Absolute figures
> for the syscall rows will move with kernel version and seccomp profile.

| Dimension | Value |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor (8 cores visible) |
| RAM | 15 GiB |
| OS / kernel | Linux 6.12.101+deb13-amd64 |
| Architecture | amd64 |
| Go toolchain | go1.27.1 linux/amd64 |
| Git branch | jaimerias-que-tu-te-connect |
| Git commit | 1da8e4c |
| Generated (UTC) | 2026-09-10 |
| Bench wall-clock | `-benchtime=1s`, median of 3 runs, 1-min load ~1.7 |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/proc/reaper
cpu: AMD EPYC 7351P 16-Core Processor

benchmark                    median ns/op   spread   B/op  allocs/op
ReapOnce_NoChildren-8               283.5     1.4%      0      0
ReapOnce_WithObserver-8             288.5     0.9%      0      0
New-8                               88.81     2.3%    104      2
New_WithOption-8                    93.45     3.2%    104      2
IsPID1-8                            119.0     0.9%      0      0
StartStop_Cycle-8                  86 000     5.3%    441      7
SetChildSubreaper-8                 273.7     1.2%      0      0
```
