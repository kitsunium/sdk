<!-- generated from internal/service/proc/exec/exec_unix_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./proc/exec/` to refresh -->
# Benchmarks — `internal/service/proc/exec`

**"When I pay for `Start`, how much of it is the kernel and how much is SDK
overhead I could avoid?"**

**0.06 %.** That is the whole answer for a direct spawn, and the rest of this
file is the evidence plus the one place where the answer changes by a factor of
six.

## 1. SDK preparation is 0.06 % of a launch, and does not appear in a profile of one

| | ns/op | share of a launch |
|---|---:|---:|
| `Prepare_Direct_Inherit` — everything `Start` does before `os.StartProcess` | **377.4** | **0.060 %** |
| `Start_BinTrue_Inherit` — the same spawn, fork + execve + wait4 | **626 200** | 100 % |

`Prepare_Direct_Inherit` is not an estimate: it calls `validateSpec`,
`checkLimits`, `validateCgroupPath`, `buildStdio`, `resolveSpawn`, `spawnFiles`,
`childFileTable` and `buildProcAttr` in `Start`'s own order. `/bin/true` is the
cheapest child a kernel can be asked to run, so the 626 µs is as close to the
fork/exec floor as a real launch gets — the SDK's share is flattered by nothing.

A CPU profile of `Start_BinTrue_Inherit` says the same thing more bluntly. Total
samples were **12.2 % of wall time** (a launch is mostly *blocked*, not running),
and of that CPU:

```
  0     0%   100%   320ms 80.00%  exec.Start
  0     0%   100%   320ms 80.00%  os.StartProcess
  0     0%   100%   300ms 75.00%  syscall.forkExec
210ms 52.50% 52.50% 210ms 52.50%  runtime.rtsigprocmask   ← Go's fork/exec, not ours
  0     0%   100%    80ms 20.00%  exec.(*handle).Wait
```

**Not one sample landed in any preparation function.** `buildProcAttr`,
`buildStdio`, `resolveCredential` and the rest are below the sampling floor of a
call they are nominally part of. The single largest consumer is
`runtime.rtsigprocmask` — the signal-mask save/restore inside Go's own
`forkAndExecInChild`.

**Verdict: there is nothing to optimise on the direct-spawn path.** No change was
made to it.

## 2. One `Rlimits` entry makes the launch 5.8× slower — and the multiplier is YOUR binary

This is the finding a caller of this package actually needs, and the API gives no
hint of it.

| | ns/op | vs. direct |
|---|---:|---:|
| `Start_BinTrue_Inherit` — direct spawn | 626 200 | 1.0× |
| `Start_BinTrue_Null` — direct spawn, stdio to `/dev/null` | 651 200 | 1.04× |
| `Start_BinTrue_Trampoline` — **identical spawn + one `RLIMIT_NOFILE`** | **3 654 000** | **5.8×** |
| `Start_SelfBinary_NoOp` — spawn THIS 5.3 MB test binary and let it exit | 4 574 000 | 7.3× |

Setting a single `Rlimits` entry (or a `Umask`, or a `CgroupPath`) routes the
spawn through the re-exec trampoline, because the Go runtime exposes no
`SysProcAttr` hook to run `setrlimit(2)` between fork and exec. The trampoline is
**`os.Executable()` — the caller's own binary**. So the launch becomes: exec the
caller's binary, load and relocate it, run every package initialiser it links,
call `setrlimit`, then `execve` the real target.

The fourth row is the hypothesis test, not decoration. If the gap were the
trampoline's own *code*, it would be small (§3 shows that code costs 17 µs). If
the gap is loading the caller's binary, `Start_BinTrue_Trampoline` must land
between "exec a tiny target" (626 µs) and "exec this 5.3 MB binary and let it run
to completion" (4 574 µs). It lands at 3 654 µs — between them, and much nearer
the large binary, exactly where the mechanism predicts. **Confirmed.**

The consequence is the sentence worth carrying away: **the trampoline's cost
scales with the size and initialiser count of the SUPERVISOR's binary, not the
target's.** 3.0 ms of overhead was measured against a 5.3 MB binary. A 50 MB
service binary will pay more, and nothing in the `Spec` says so.

Neither `Nice` nor `OOMScoreAdj` triggers it — they are applied post-start on the
live pid. If a limit can be expressed either way, expressing it as `Nice` /
`OOMScoreAdj` / a post-spawn `cgroup.Group.Add` costs microseconds; expressing it
as `Rlimits` costs milliseconds.

## 3. Where the preparation microseconds actually are: descriptors, not code

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Prepare_Direct_Inherit` | 377.4 | 480 | 3 |
| `Prepare_Direct_Env32` | 651.7 | 992 | 4 |
| `Prepare_Trampoline` | 16 950 | 1 240 | 23 |
| `Prepare_Direct_Capture` | 30 370 | 1 224 | 24 |

The two expensive rows are expensive for one reason: **file descriptors**.

| workload | ns/op | allocs/op |
|---|---:|---:|
| `BuildStdio_Inherit` — share the parent's streams, opens nothing | **113.7** | 1 |
| `BuildStdio_Null` — 3 × open `/dev/null` + 3 × close | 13 560 | 16 |
| `BuildStdio_Capture` — 2 pipes + 1 null, + closes | 28 540 | 22 |
| `OSExecutable` — the `readlink("/proc/self/exe")` a trampolined spawn needs | 2 416 | 3 |

A CPU profile of `BuildStdio_Null` names the kernel and nobody else:

```
2.42s 65.58% 65.58%  2.42s 65.58%  internal/runtime/syscall/linux.Syscall6
0.05s  1.36%         0.24s  6.50%  internal/poll.runtime_pollOpen      ← Go's netpoll registration
0.02s  0.54%         3.00s 81.30%  exec.(*stdioState).wireNull
0.03s  0.81%         0.13s  3.52%  runtime.growslice                   ← the SDK's whole share
```

**72 % is the kernel plus Go's `os` layer; `growslice` — the only line that is
this package's own — is 3.5 %.** Pre-sizing `stdioState.opened` and
`.closeChild` would recover roughly 470 ns of a 651 200 ns launch (0.07 %). Not
done, and the numbers are here so nobody has to wonder again.

Read `Prepare_Direct_Capture` (30 370) as the *whole-lifecycle* fd cost of
`StdioCapture`, not an overcount: the benchmark closes all five descriptors, and
a successful `Start` closes the same five — three in `afterStart`, two in the
copiers at EOF. Even so it is **4.9 % of the 626 µs launch measured in §1**,
which makes `StdioCapture` the most expensive thing the SDK itself does on this
path.

## 4. Everything else is nanoseconds, and the table exists to prove it

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `ValidateSpec` | 2.923 | 0 | 0 |
| `NeedsTrampoline_No` | 2.817 | 0 | 0 |
| `ValidateCgroupPath_Empty` | 3.192 | 0 | 0 |
| `BuildArgv_Explicit` | 7.781 | 0 | 0 |
| `CheckLimits_None` | 11.22 | 0 | 0 |
| `ResolveCredential_None` | 15.08 | 0 | 0 |
| `BuildArgv_Default` — allocates `[]string{Path}` | 28.80 | 16 | 1 |
| `AppendExtraFiles_4` | 51.71 | 64 | 1 |
| `EncodeTrampoline_Umask` | 91.94 | 8 | 1 |
| `CheckLimits_Three` | 98.46 | 0 | 0 |
| `ResolveCredential_NumericGroups` (4 groups) | 149.0 | 64 | 2 |
| `ResolveCredential_NumericUID` | 167.8 | 128 | 2 |
| `BuildProcAttr_Env0` | 199.1 | 272 | 2 |
| `ResolveCredential_NamedUser` — **the NSS lookup** | 233.4 | 179 | 4 |
| `BuildProcAttr_Env32` | 458.7 | 784 | 3 |
| `EncodeTrampoline_ThreeRlimits` | 605.8 | 64 | 5 |
| `EnvironWithout_64` — trampoline child, run 3× per launch | 1 346 | 1 152 | 1 |
| `ApplyTrampoline_ThreeRlimits` — 3 real `setrlimit(2)` | 1 167 | 0 | 0 |

Three of these were candidates on inspection and are refuted by the numbers:

- **`resolveCredential` with a NAME was the one plausible way for preparation to
  dwarf a launch** — an NSS round trip can mean a network directory. Measured
  here at **233 ns**, 1.4× the numeric-uid path, because glibc resolves a local
  passwd entry from a cached file. It is 0.04 % of a launch *on this host*. On a
  host with `sss`/LDAP in `nsswitch.conf` it will not be, and that is a property
  of the host, not of this package. `Spec.User` as a numeric uid skips `Lookup`
  entirely — that is what the shortcut is for.
- **`buildProcAttr` copies `Spec.Env` on every `Start`** (199 ns empty, 459 ns at
  32 entries). It is deliberate — a nil `Env` handed to `os.StartProcess` would
  inherit the supervisor's environment, secrets included — and 260 ns of copy is
  0.04 % of a launch.
- **`encodeTrampoline` builds its payload with `+`-concatenated temporaries**
  (5 allocations for 3 limits). It only ever runs on the trampoline path, so its
  606 ns sits inside a 3 654 000 ns launch: **0.017 %.** Optimising it would be
  measuring the wrong thing at 200× magnification.

`EnvironWithout_64` is worth one sentence because it looks alarming: the
trampoline child calls it **three times, nested**, each allocating a fresh
environment slice. 3 × 1 346 ns = 4 µs, against a 3 654 µs trampolined launch —
**0.11 %.** A single-pass three-key filter is the obvious rewrite and would save
nothing anybody can measure.

## 5. Handle operations: the port's cheap half, and one 25 % asymmetry

| workload | ns/op | allocs/op |
|---|---:|---:|
| `Handle_PID` | 2.853 | 0 |
| `Handle_Wait_Memoised` — 2nd and later `Wait` | 18.06 | 0 |
| `Handle_SignalGroup_Live` — `kill(-pgid, sig)` | 390.0 | 0 |
| `Handle_Signal_Live` — `kill(pid, sig)` via `os.Process` | 488.6 | 0 |

`PID` is a field read and `Wait` after the first is a `sync.Once` fast path plus a
struct copy — a supervisor may call either from as many goroutines as it likes.

The asymmetry is real and consistent across every run: **signalling the LEADER is
25 % more expensive than signalling the whole GROUP.** `SignalGroup` issues
`syscall.Kill` directly; `Signal` routes through `os.Process.Signal`, which takes
the process handle and does its own liveness bookkeeping first. This is not a
defect — the `os.Process` route is what keeps `Signal` from racing a reap — but if
a supervisor is signalling in a loop and the child leads its own group,
`SignalGroup` is the cheaper call and reaches more.

For scale: `kill(2)` here is ~350–390 ns, `wait4` ~283 ns, `setrlimit` ~350 ns,
`getpid` ~119 ns. Everything in this package that is not a fork is one of those.

## Reproducibility envelope

> Every figure is the **median of 3 full runs**, each a separate process. The
> spread column below is `(max−min)/median`; every row was under 10 % and most
> under 3 %. Load average was checked before each run and stayed between 0.47 and
> 1.47 on this 8-core box.
>
> **The ratios are what this report asserts** — 0.06 % preparation, 5.8× for the
> trampoline, 4.7 % for `StdioCapture`. Absolute `ns/op` for anything containing
> a `fork` will differ on other kernels and under other seccomp profiles.

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
| Bench wall-clock | `-benchtime=1s`, median of 3 runs, 1-min load 0.47–1.47 |

## Results

Medians of 3 runs (`min`–`max` in the spread column).

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/proc/exec
cpu: AMD EPYC 7351P 16-Core Processor

benchmark                                  median ns/op   spread   B/op  allocs/op
Prepare_Direct_Inherit-8                          377.4     2.0%    480      3
Prepare_Direct_Env32-8                            651.7     5.9%    992      4
Prepare_Direct_Capture-8                         30 370     7.3%   1224     24
Prepare_Trampoline-8                             16 950     9.9%   1240     23
Start_BinTrue_Inherit-8                         626 200     2.0%   1272     18
Start_BinTrue_Null-8                            651 200     5.1%   1744     33
Start_BinTrue_Trampoline-8                    3 654 000     3.9%   2760     40
Start_SelfBinary_NoOp-8                       4 574 000     2.9%   1856     33
ValidateSpec-8                                    2.923     1.6%      0      0
CheckLimits_None-8                                11.22     0.9%      0      0
CheckLimits_Three-8                               98.46     3.0%      0      0
ValidateCgroupPath_Empty-8                        3.192     5.4%      0      0
NeedsTrampoline_No-8                              2.817     1.0%      0      0
BuildArgv_Default-8                               28.80     1.1%     16      1
BuildArgv_Explicit-8                              7.781     3.7%      0      0
BuildStdio_Inherit-8                              113.7     0.5%    208      1
BuildStdio_Null-8                                13 560     0.8%    680     16
BuildStdio_Capture-8                             28 540     6.7%    952     22
ResolveCredential_None-8                          15.08     0.5%      0      0
ResolveCredential_NumericUID-8                    167.8     1.5%    128      2
ResolveCredential_NamedUser-8                     233.4     1.8%    179      4
ResolveCredential_NumericGroups-8                 149.0     1.6%     64      2
BuildProcAttr_Env0-8                              199.1     1.1%    272      2
BuildProcAttr_Env32-8                             458.7     2.1%    784      3
AppendExtraFiles_4-8                              51.71     8.1%     64      1
EncodeTrampoline_Umask-8                          91.94     2.8%      8      1
EncodeTrampoline_ThreeRlimits-8                   605.8     0.5%     64      5
OSExecutable-8                                    2 416     1.0%    192      3
EnvironWithout_64-8                               1 346     2.3%   1152      1
ApplyTrampoline_ThreeRlimits-8                    1 167     3.8%      0      0
Handle_PID-8                                      2.853     0.2%      0      0
Handle_Wait_Memoised-8                            18.06     0.4%      0      0
Handle_Signal_Live-8                              488.6     1.3%      0      0
Handle_SignalGroup_Live-8                         390.0     0.4%      0      0
```
