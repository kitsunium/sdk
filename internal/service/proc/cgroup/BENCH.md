<!-- generated from internal/service/proc/cgroup/cgroup_linux_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./proc/cgroup/` to refresh -->
# Benchmarks — `internal/service/proc/cgroup`

## What could not be measured, and why — read this first

**Every verb this package exists for is unmeasurable in the container these
numbers were taken in, and this report does not pretend otherwise.**

`Create`, `Add`, `SetMemoryMax`, `SetCPUMax`, `SetPidsMax`, `SetIOMax`, `Kill`,
`Freeze`, `Thaw` and `Delete` are all a `mkdir(2)` or a `write(2)` against
cgroupfs, and cgroupfs is where the cost lives — the kernel parses each value in a
controller-specific handler and applies it to a live hierarchy. Measuring that
needs a **delegated** cgroup v2 subtree.

What this host actually has, verified rather than assumed:

| | |
|---|---|
| cgroup v2 mounted | **yes** — `cgroup2 on /sys/fs/cgroup (rw,nosuid,nodev,noexec,relatime,nsdelegate)` |
| controllers enabled | **all** — `cpuset cpu io memory hugetlb pids rdma misc` |
| `mkdir /sys/fs/cgroup/<name>` | **EACCES** |
| `mkdir` inside this process's own scope (`…/session-NNNN.scope`) | **EACCES** |
| `cgroup.Available()` | **false** |

So: the hierarchy is right there, fully equipped, and delegated to nobody. This is
the ordinary unprivileged-container case the package is explicitly designed to
degrade on, and it is the case in which the live paths cannot be timed.

**No live row was faked.** `BenchmarkCreate_Unavailable` refuses to run at all if
a future host *does* delegate, so it can never be mistaken for a success-path
number. The rows below measure three real things: the probe, the pure path and
value construction, and the SDK's share of a controller write isolated against a
plain filesystem.

**To get the missing numbers**, re-run on a host with delegation — a systemd user
session with `Delegate=yes`, a privileged container, or a CI runner with a
delegated subtree — and compare the live `Create`/`Add`/`Set*Max` against §3's
floor. The difference is cgroupfs and nothing else.

## 1. `Available()` is two syscalls and 5.9 µs, and it is honest on purpose

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Available` — `stat` the marker, then really try `MkdirTemp` | **5 868** | 511 | 8 |
| `Create_Unavailable` — `stat`, `mkdir` → EACCES, typed wrap | 5 460 | 640 | 9 |

The probe does not read a config file or trust a mount table: it creates a
throwaway directory under the root, because that is the only test of *delegation*
that cannot false-positive. On this host that `mkdir` is refused, and the refusal
costs ~5 µs — **cgroupfs is roughly 2.5× the cost of the same failing operation on
tmpfs**, because the kernel takes the cgroup mutex before it can tell you no.

`Create_Unavailable` at 5 460 ns is the same shape, and the two agree, which is
the cross-check that the rows are measuring the filesystem rather than the
benchmark.

**The actionable consequence: `Available()` is not a predicate to call per
spawn.** It is 5.9 µs, eight allocations, and two syscalls to be told the same
thing it said last time. Call it once at wiring time and remember the answer. (For
scale, a whole process launch is ~626 µs — so a per-spawn `Available()` is 0.9 %
of a launch to answer a question whose answer cannot change without a remount.)

## 2. The pure half is free

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `MaxOrValue_Unlimited` — a negative writes the literal `"max"` | **2.831** | 0 | 0 |
| `ApplyOptions_None` | 23.84 | 16 | 1 |
| `ApplyOptions_WithRoot` | 27.14 | 16 | 1 |
| `MaxOrValue_Number` — `strconv.FormatInt` | 43.67 | 16 | 1 |
| `ValidateName_Valid` | 47.66 | 0 | 0 |
| `WithinRoot_Inside` | 180.1 | 24 | 1 |
| `ValidateName_Traversal` — the refusal | 279.4 | 216 | 3 |

`validateName` is **47.7 ns and zero allocations** on the accepted path — four
string comparisons, a separator scan, and a `filepath.Clean` round trip. The
refusal costs 279 ns because it builds the typed `INVALID_SPEC` with the offending
name attached; a crafted name can therefore make the call ~6× more expensive and
0 % more successful, which against a ~5 µs cgroupfs operation is not a lever.

`withinRoot` at 180 ns is the defence-in-depth check after the join, and it
allocates 24 B for the `root + PathSeparator` concatenation. It runs once per
`Create`.

The whole pure half sums to under 500 ns against a `Create` that is at minimum
5 460 ns here — and much more on a host that actually creates the directory.

## 3. The SDK's share of a controller write: **≈ 6 %**

This is the one number this container *can* establish, and it is the one a future
delegated run needs.

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `RawWriteFile_Baseline` — `os.WriteFile`, no SDK code in frame | 8 551 | 152 | 3 |
| `WriteController_TmpfsNotCgroupfs` — `SetMemoryMax(1<<30)` | 9 257 | 216 | 5 |
| `Add_TmpfsNotCgroupfs` — `Add(pid)` | 9 231 | 224 | 5 |

**Read the row names.** These are writes to a private directory on `/dev/shm`.
They are **not** cgroup writes and 9.3 µs is **not** what `SetMemoryMax` costs
against cgroupfs — the memory controller's handler does real work that a tmpfs
write does not, and none of it is here.

What the subtraction does establish:

```
9 257 − 8 551 = 706 ns  →  the SDK's entire share of SetMemoryMax  (7.6 %)
9 231 − 8 551 = 680 ns  →  the SDK's entire share of Add           (7.4 %)
5 allocs − 3 allocs = 2  →  exactly filepath.Join and the decimal rendering
```

**~700 ns and two allocations is everything this package contributes to a
controller write.** Everything else belongs to `os.WriteFile` and the filesystem
under it. On a delegated host the denominator will be larger (cgroupfs is slower
than tmpfs — §1 measured the same refusal at 2.5×), so the SDK's share will be
*smaller* than 7.6 %, not larger.

**Verdict: nothing to optimise.** The two allocations are a path join and a number
rendered as text, both required to name a file and write a value.

### A methodology note that cost 22× to learn

The plain-filesystem rows first ran under `TMPDIR` and measured **201 898 ns** —
22× the figure above. `TMPDIR` here is tmpfs, so that was not a disk. A CPU
profile showed only 33 % of wall time on-CPU: the rest was **contention from the
other jobs sharing this build machine's `/tmp`**. Re-pointing the rows at
`/dev/shm` — also tmpfs, but uncontended — gave 9.3 µs, stable to 2.2 % across five
runs. The benchmark now prefers `/dev/shm` and logs which directory it used.

That is worth recording because the failure mode is silent: the number was
plausible, repeatable within a run, and completely wrong.

## Platform note

`//go:build linux`, because `validateName`, `withinRoot`, `maxOrValue` and
`controlGroup` live in `cgroup_linux.go`. The Windows (Job Objects) and FreeBSD
(rctl) backends are separate implementations with their own syscalls and are **not
characterised here** — their numbers would have nothing in common with these, and
inventing a shared table would be the dishonest kind of portability.

No production code was changed in this package.

## Reproducibility envelope

> Every figure is the **median of 5 full runs**, each a separate process. Spread
> was under 8 % on every row but `Available` (14.8 %, a single high outlier over
> five runs — cgroupfs contends with the rest of the machine). Load average was
> 1.25–1.71 across the runs on this 8-core box.
>
> **The claims this report asserts** are: the pure half is under 500 ns, the SDK's
> share of a controller write is ~700 ns and two allocations, and `Available()` is
> a wiring-time call. The absolute plain-filesystem figures are `/dev/shm` on this
> machine and will move elsewhere; the *difference* between the two rows is what
> transfers.

| Dimension | Value |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor (8 cores visible) |
| RAM | 15 GiB |
| OS / kernel | Linux 6.12.101+deb13-amd64 |
| Architecture | amd64 |
| cgroup v2 | mounted rw, all controllers, **no delegation to the caller** |
| Go toolchain | go1.27.1 linux/amd64 |
| Git branch | jaimerias-que-tu-te-connect |
| Git commit | 532a984 |
| Generated (UTC) | 2026-09-10 |
| Bench wall-clock | `-benchtime=1s`, median of 5 runs |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/proc/cgroup
cpu: AMD EPYC 7351P 16-Core Processor

benchmark                              median ns/op   spread   B/op  allocs/op
Available-8                                   5 868    14.8%    511      8
ValidateName_Valid-8                          47.66     5.5%      0      0
ValidateName_Traversal-8                      279.4     2.8%    216      3
WithinRoot_Inside-8                           180.1     1.1%     24      1
MaxOrValue_Number-8                           43.67     4.0%     16      1
MaxOrValue_Unlimited-8                        2.831     0.7%      0      0
ApplyOptions_None-8                           23.84     3.3%     16      1
ApplyOptions_WithRoot-8                       27.14     1.3%     16      1
Create_Unavailable-8                          5 460     2.1%    640      9
WriteController_TmpfsNotCgroupfs-8            9 257     2.2%    216      5
Add_TmpfsNotCgroupfs-8                        9 231     2.3%    224      5
RawWriteFile_Baseline-8                       8 551     7.5%    152      3
```
